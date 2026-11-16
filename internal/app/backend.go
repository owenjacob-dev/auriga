package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// Backend is everything the UI needs from the outside world. All methods
// except Events may block and are only called from tea.Cmds, never from
// Update.
type Backend interface {
	// Events delivers connection state changes and prompts.
	Events() <-chan sshx.Event
	// ListAliases reads the concrete Host aliases from ~/.ssh/config.
	ListAliases() (aliases []string, warning string, err error)
	Connect(alias string)
	Disconnect(alias string)
	Reconnect(alias string)
	Probe(ctx context.Context, alias, configPath, service string) (remote.ProbeFacts, error)
	ReadConfig(ctx context.Context, alias, configPath string) (remote.RedactedConfig, error)
	ListBackups(ctx context.Context, alias, configPath string) ([]remote.Backup, error)
	LoadInventory() (*inventory.Inventory, error)
	LoadSettings() (inventory.Settings, error)
	SaveInventory(*inventory.Inventory) error
	// AdminClient returns a client for the host's admin endpoint. It does no
	// I/O until a request is made.
	AdminClient(alias string, port int, configPath, service string) *corehttp.Client
	// ResetAdmin forgets the host's transport choice (tunnel or fallback).
	ResetAdmin(alias string)

	// Wizard support.
	LoadDraft(org string) (*wizard.Draft, bool, error)
	SaveDraft(*wizard.Draft) error
	LoadPreset(n presets.Network) (*presets.Preset, error)
	SavePreset(*presets.Preset) error
	// FetchPreset downloads a preset's upstream file over HTTPS from the
	// operator's machine. It is the only network call besides SSH, and runs
	// only when the operator asks.
	FetchPreset(ctx context.Context, n presets.Network) ([]byte, error)
	CheckArchive(ctx context.Context, alias string, spec stellarcfg.ArchiveSpec, publish bool) (remote.ArchiveCheck, error)

	// Deploy support.
	DeployOps() deploy.Ops
	AuditLog() AuditLog
	// Close disconnects everything and zeroes cached secrets.
	Close()
}

// AuditLog appends to and reads the audit log; *audit.Log implements it.
type AuditLog interface {
	Append(audit.Entry) error
	Read() ([]audit.Entry, int, error)
	Path() string
}

// LiveBackend is the real Backend: an SSH pool, the helper runner, the
// ssh config resolver and the inventory file.
type LiveBackend struct {
	env       sshx.Env
	pool      *sshx.Pool
	runner    *remote.Runner
	resolver  *sshx.Resolver
	configDir string
	audit     *audit.Log

	adminMu sync.Mutex
	admin   map[adminKey]*corehttp.Client
}

// NewLiveBackend builds the backend for this process.
func NewLiveBackend(env sshx.Env, agentSock string, dirs xdg.Dirs, log *slog.Logger) *LiveBackend {
	resolver := sshx.NewResolver(env)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: resolver, AgentSock: agentSock, Log: log})
	return &LiveBackend{
		env:       env,
		pool:      pool,
		runner:    remote.NewRunner(pool, pool),
		resolver:  resolver,
		configDir: dirs.Config,
		audit:     audit.Open(dirs.State),
		admin:     map[adminKey]*corehttp.Client{},
	}
}

func (b *LiveBackend) Events() <-chan sshx.Event { return b.pool.Events() }

func (b *LiveBackend) ListAliases() ([]string, string, error) {
	a, err := sshx.ListAliases(b.env)
	return a, b.resolver.Warning, err
}

func (b *LiveBackend) Connect(alias string) { b.pool.Add(alias) }

func (b *LiveBackend) Disconnect(alias string) {
	b.pool.Remove(alias)
	b.runner.Forget(alias)
	b.forgetAdmin(alias)
}

func (b *LiveBackend) ResetAdmin(alias string) { b.forgetAdmin(alias) }

func (b *LiveBackend) Reconnect(alias string) { b.pool.Reconnect(alias) }

func (b *LiveBackend) Probe(ctx context.Context, alias, configPath, service string) (remote.ProbeFacts, error) {
	return b.runner.Probe(ctx, alias, configPath, service)
}

func (b *LiveBackend) ReadConfig(ctx context.Context, alias, configPath string) (remote.RedactedConfig, error) {
	return b.runner.ReadConfig(ctx, alias, configPath)
}

func (b *LiveBackend) ListBackups(ctx context.Context, alias, configPath string) ([]remote.Backup, error) {
	return b.runner.ListBackups(ctx, alias, configPath)
}

func (b *LiveBackend) LoadInventory() (*inventory.Inventory, error) {
	return inventory.Load(b.configDir)
}

func (b *LiveBackend) LoadSettings() (inventory.Settings, error) {
	return inventory.LoadSettings(b.configDir)
}

func (b *LiveBackend) SaveInventory(inv *inventory.Inventory) error {
	return inv.Save(b.configDir)
}

func (b *LiveBackend) LoadDraft(org string) (*wizard.Draft, bool, error) {
	return wizard.LoadDraft(b.configDir, org)
}

func (b *LiveBackend) SaveDraft(d *wizard.Draft) error { return wizard.SaveDraft(b.configDir, d) }

func (b *LiveBackend) LoadPreset(n presets.Network) (*presets.Preset, error) {
	return presets.Load(b.configDir, n)
}

func (b *LiveBackend) SavePreset(p *presets.Preset) error {
	return presets.SaveOverride(b.configDir, p)
}

// maxPresetSize bounds a preset download.
const maxPresetSize = 1 << 20

func (b *LiveBackend) FetchPreset(ctx context.Context, n presets.Network) ([]byte, error) {
	u := presets.SourceURL(n)
	if !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("refusing to fetch %q: not https", u)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "auriga")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxPresetSize))
}

func (b *LiveBackend) CheckArchive(ctx context.Context, alias string, spec stellarcfg.ArchiveSpec, publish bool) (remote.ArchiveCheck, error) {
	return b.runner.CheckArchive(ctx, alias, spec.WellKnownURL(), spec.Binaries(publish))
}

func (b *LiveBackend) DeployOps() deploy.Ops { return liveOps{b} }

func (b *LiveBackend) AuditLog() AuditLog { return b.audit }

func (b *LiveBackend) Close() {
	b.runner.Close()
	b.pool.Close()
}
