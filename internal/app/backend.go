package app

import (
	"context"
	"log/slog"
	"sync"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
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
	LoadInventory() (*inventory.Inventory, error)
	SaveInventory(*inventory.Inventory) error
	// AdminClient returns a client for the host's admin endpoint. It does no
	// I/O until a request is made.
	AdminClient(alias string, port int, configPath string) *corehttp.Client
	// ResetAdmin forgets the host's transport choice (tunnel or fallback).
	ResetAdmin(alias string)
	// Close disconnects everything and zeroes cached secrets.
	Close()
}

// LiveBackend is the real Backend: an SSH pool, the helper runner, the
// ssh config resolver and the inventory file.
type LiveBackend struct {
	env       sshx.Env
	pool      *sshx.Pool
	runner    *remote.Runner
	resolver  *sshx.Resolver
	configDir string

	adminMu sync.Mutex
	admin   map[adminKey]*corehttp.Client
}

// NewLiveBackend builds the backend for this process.
func NewLiveBackend(env sshx.Env, agentSock, configDir string, log *slog.Logger) *LiveBackend {
	resolver := sshx.NewResolver(env)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: resolver, AgentSock: agentSock, Log: log})
	return &LiveBackend{
		env:       env,
		pool:      pool,
		runner:    remote.NewRunner(pool, pool),
		resolver:  resolver,
		configDir: configDir,
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

func (b *LiveBackend) LoadInventory() (*inventory.Inventory, error) {
	return inventory.Load(b.configDir)
}

func (b *LiveBackend) SaveInventory(inv *inventory.Inventory) error {
	return inv.Save(b.configDir)
}

func (b *LiveBackend) Close() {
	b.runner.Close()
	b.pool.Close()
}
