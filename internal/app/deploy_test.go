package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/exp/golden"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

// scriptedOps is a deploy.Ops for UI tests: every call succeeds unless
// told otherwise; sync takes a few info polls.
type scriptedOps struct {
	mu       sync.Mutex
	calls    []string
	fail     map[string]error
	catching map[string]int // info polls before Synced!
	staged   map[string]deploy.Target
	seeds    map[string]string // what Stage received on "stdin"
	hold     chan struct{}     // when set, Info waits on it during sync
}

func newScriptedOps() *scriptedOps {
	return &scriptedOps{fail: map[string]error{}, catching: map[string]int{}, staged: map[string]deploy.Target{}, seeds: map[string]string{}}
}

func (o *scriptedOps) call(t deploy.Target, op string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, t.Alias+"/"+op)
	return o.fail[t.Alias+"/"+op]
}

func (o *scriptedOps) Calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

func (o *scriptedOps) Preflight(_ context.Context, t deploy.Target) (remote.Preflight, error) {
	return remote.Preflight{UnitLoad: "loaded", Active: "active", ConfigFSAvailKB: 1 << 20, WorkDirAvailKB: 1 << 30}, o.call(t, "preflight")
}

func (o *scriptedOps) GenSeed(_ context.Context, t deploy.Target) (string, error) {
	return pubC, o.call(t, "gen-seed")
}

func (o *scriptedOps) Stage(_ context.Context, t deploy.Target) (remote.Staged, error) {
	if err := o.call(t, "stage"); err != nil {
		return remote.Staged{}, err
	}
	o.mu.Lock()
	o.staged[t.Alias] = t
	if t.Import != nil {
		o.seeds[t.Alias] = string(t.Import.Bytes())
	}
	o.mu.Unlock()
	pk := ""
	if t.Seed != nil {
		pk = t.Seed.PublicKey
	}
	return remote.Staged{SHA256: strings.Repeat("a", 64), PublicKey: pk}, nil
}

func (o *scriptedOps) StageBackup(_ context.Context, t deploy.Target) (string, error) {
	return strings.Repeat("a", 64), o.call(t, "stage-backup "+t.RestoreFrom)
}

func (o *scriptedOps) ReadStaged(_ context.Context, t deploy.Target) (remote.RedactedConfig, error) {
	if err := o.call(t, "read-staged"); err != nil {
		return remote.RedactedConfig{}, err
	}
	text := t.Rendered
	if t.RestoreFrom != "" {
		text = t.Expected
	} else if t.Seed != nil {
		s := *t.Seed
		text = []byte(strings.Replace(string(text), `NODE_SEED = "`+s.SeedMarker()+`"`, `NODE_SEED = "`+(stellarcfg.Seed{PublicKey: s.PublicKey, Alias: s.Alias}).Redacted()+`"`, 1))
	}
	return remote.RedactedConfig{Text: text}, nil
}

func (o *scriptedOps) Backup(_ context.Context, t deploy.Target) (string, error) {
	return t.ConfigPath + ".auriga-20261116T120000Z.bak", o.call(t, "backup")
}

func (o *scriptedOps) Install(_ context.Context, t deploy.Target) (string, error) {
	if err := o.call(t, "install"); err != nil {
		return "", err
	}
	return strings.Repeat("a", 64), nil
}

func (o *scriptedOps) Init(_ context.Context, t deploy.Target, i deploy.Init) error {
	return o.call(t, string(i.Kind))
}

func (o *scriptedOps) Restart(_ context.Context, t deploy.Target) (string, error) {
	return "active", o.call(t, "restart")
}

func (o *scriptedOps) Rollback(_ context.Context, t deploy.Target, backup string) (string, error) {
	return "active", o.call(t, "rollback")
}

func (o *scriptedOps) Active(_ context.Context, t deploy.Target) (string, error) {
	return "active", o.call(t, "active")
}

func (o *scriptedOps) Info(_ context.Context, t deploy.Target) (*corehttp.Info, error) {
	if err := o.call(t, "info"); err != nil {
		return nil, err
	}
	o.mu.Lock()
	n := o.catching[t.Alias]
	if n > 0 {
		o.catching[t.Alias] = n - 1
	}
	hold := o.hold
	o.mu.Unlock()
	if n > 0 {
		if hold != nil {
			<-hold
		}
		return &corehttp.Info{State: "Catching up", Ledger: corehttp.LedgerInfo{Num: 4100, Age: 12}}, nil
	}
	return &corehttp.Info{State: "Synced!", Ledger: corehttp.LedgerInfo{Num: 4242, Age: 2}}, nil
}

func (o *scriptedOps) Quorum(_ context.Context, t deploy.Target) (*corehttp.QuorumResponse, error) {
	if err := o.call(t, "quorum"); err != nil {
		return nil, err
	}
	yes, two := true, 2
	return &corehttp.QuorumResponse{QSet: &corehttp.QSetStatus{FailAt: &two}, Transitive: &corehttp.Transitive{Intersection: &yes}}, nil
}

type memAuditLog struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (l *memAuditLog) Append(e audit.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.Time.IsZero() {
		e.Time = time.Date(2026, 11, 16, 12, 0, len(l.entries), 0, time.UTC)
	}
	l.entries = append(l.entries, e)
	return nil
}

func (l *memAuditLog) Path() string { return "/state/auriga/audit.log" }

func (l *memAuditLog) Read() ([]audit.Entry, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := slices.Clone(l.entries)
	slices.Reverse(out)
	return out, 0, nil
}

const pubC = "GDVEU3DD4KOFECV66VIHWEZOYX4ZKR3WV27L464SIIPOU2IUI3JCZA57"

// deployFromReview continues the testnet Basic wizard run: D opens the
// deploy confirmation, esc backs out without touching a host, then the
// deploy runs on the fake ops and the outcome is shown.
func deployFromReview(t *testing.T, m Model, b *fakeBackend, plan *wizard.Plan) Model {
	t.Helper()
	m = keys(t, m, "D")
	out := screen(m)
	if !strings.Contains(out, "Deploy acme to 3 host(s) on testnet") || !strings.Contains(out, "new-db: a rollback cannot undo this") {
		t.Fatalf("no deploy confirmation:\n%s", out)
	}
	t.Run("deploy-confirm", func(t *testing.T) { golden.RequireEqual(t, out) })
	pending := m.pending
	m = keys(t, m, "esc")
	if _, ok := m.top().(*wizard.Model); !ok || m.pending != nil || len(b.ops.Calls()) != 0 {
		t.Fatal("backing out of the confirmation did not return to the review untouched")
	}
	for _, tg := range pending.targets {
		if tg.Import.Len() != 0 {
			t.Errorf("%s: seed copy not wiped after backing out", tg.Alias)
		}
	}

	cmdWait = 500 * time.Millisecond
	defer func() { cmdWait = 15 * time.Millisecond }()
	m = keys(t, m, "D", "tab", "tab", "enter") // tab twice: back to one at a time
	if m.run != nil {
		t.Fatalf("deploy did not finish:\n%s", screen(m))
	}
	out = screen(m)
	if !strings.Contains(out, "Finished: 3 of 3 deployed") || !strings.Contains(out, "● acme-a deployed") {
		t.Fatalf("deploy result:\n%s", out)
	}
	t.Run("deploy-done", func(t *testing.T) { golden.RequireEqual(t, out) })

	calls := b.ops.Calls()
	if i, j := slices.Index(calls, "acme-a/quorum"), slices.Index(calls, "acme-b/preflight"); i < 0 || j < i {
		t.Errorf("not one at a time: %v", calls)
	}
	for _, hp := range plan.Hosts {
		tg := b.ops.staged[hp.Alias]
		if b.ops.seeds[hp.Alias] != wizardSeeds[hp.Alias] {
			t.Errorf("%s: stage did not receive the pasted seed", hp.Alias)
		}
		if tg.Import.Len() != 0 {
			t.Errorf("%s: seed copy not wiped after staging", hp.Alias)
		}
		if !slices.Contains(calls, hp.Alias+"/new-db") {
			t.Errorf("%s: new-db not run", hp.Alias)
		}
		if hv := m.fleet.Host(hp.Alias); hv.Deploying {
			t.Errorf("%s still marked deploying", hp.Alias)
		}
	}
	if n := len(b.audit.entries); n != 3*5 {
		t.Errorf("%d audit entries", n)
	}
	m = keys(t, m, "esc")
	if _, ok := m.top().(*wizard.Model); !ok {
		t.Fatal("closing the deploy did not return to the wizard")
	}
	return m
}

// TestRestoreBackupThenAuditLog is the manual rollback: Config tab → R →
// pick a backup → its diff → the deploy screen runs steps 4 to 10 with it;
// then l on the dashboard shows the steps in the audit log.
func TestRestoreBackupThenAuditLog(t *testing.T) {
	cmdWait = 15 * time.Millisecond
	t.Cleanup(func() { cmdWait = 100 * time.Millisecond })
	const bak = "/etc/stellar/stellar-core.cfg.auriga-20261101T090000Z.bak"
	b := newFakeBackend(testInventory("acme-a"))
	b.facts["acme-a"] = healthyFacts
	now := cfgText("acme.example", "Test SDF Network ; September 2015")
	old := bytes.Replace(now, []byte("HTTP_PORT=11626"), []byte("HTTP_PORT=11626\nCATCHUP_RECENT=100"), 1)
	b.configs["acme-a"] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, NodeAlias: "acme-a", Text: now}
	b.configs["acme-a|"+bak] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, NodeAlias: "acme-a", Text: old}
	b.backups["acme-a"] = []remote.Backup{{Path: bak, Time: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), Size: int64(len(old)), Owner: "root:stellar", Mode: "640"}}
	m := startModel(t, b)
	m = step(t, m, connected("acme-a"))

	m = keys(t, m, "enter", "tab", "tab", "tab", "tab") // detail → Config tab
	if out := screen(m); !strings.Contains(out, "R restore a backup") {
		t.Fatalf("no restore key on the Config tab:\n%s", out)
	}
	m = keys(t, m, "R", "enter")
	out := screen(m)
	if !strings.Contains(out, "Restore "+bak) || !strings.Contains(out, "+CATCHUP_RECENT=100") {
		t.Fatalf("restore diff:\n%s", out)
	}
	t.Run("restore-diff", func(t *testing.T) { golden.RequireEqual(t, out) })
	if len(b.ops.Calls()) != 0 {
		t.Fatal("a host changed before the restore was confirmed")
	}

	cmdWait = 500 * time.Millisecond
	m = keys(t, m, "enter")
	if out := screen(m); !strings.Contains(out, "Restore acme-a from stellar-core.cfg.auriga-20261101T090000Z.bak") || !strings.Contains(out, "1 changed line") {
		t.Fatalf("restore confirmation:\n%s", out)
	}
	m = keys(t, m, "enter")
	cmdWait = 15 * time.Millisecond
	if out := screen(m); !strings.Contains(out, "Finished: 1 of 1 deployed") {
		t.Fatalf("restore result:\n%s", out)
	}
	if calls := b.ops.Calls(); !slices.Contains(calls, "acme-a/stage-backup "+bak) || slices.Contains(calls, "acme-a/stage") {
		t.Errorf("calls %v", calls)
	}

	m = keys(t, m, "esc", "esc", "esc", "esc", "l")
	out = screen(m)
	for _, want := range []string{"restore.stage", "restore.install", "bak 20261101T0900", "4 entries · /state/auriga/audit.log"} {
		if !strings.Contains(out, want) {
			t.Errorf("audit log lacks %q:\n%s", want, out)
		}
	}
	t.Run("audit-log", func(t *testing.T) { golden.RequireEqual(t, out) })
	m = keys(t, m, "/", "install", "enter")
	if out := screen(m); !strings.Contains(out, "1 of 4 entries match") || strings.Contains(out, "restore.stage") {
		t.Errorf("filter:\n%s", out)
	}
}
