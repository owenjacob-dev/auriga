package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

const (
	pkA   = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	pkB   = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
	pkC   = "GDVEU3DD4KOFECV66VIHWEZOYX4ZKR3WV27L464SIIPOU2IUI3JCZA57"
	sha   = "9f2c000000000000000000000000000000000000000000000000000000000000"
	seedB = "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI"
)

var errBoom = errors.New("boom")

// fakeOps records calls and fails where told. Per-host behaviour is keyed
// by "alias/op".
type fakeOps struct {
	mu      sync.Mutex
	calls   []string
	fail    map[string]error
	active  map[string]string // unit state from Active; default "active"
	states  map[string][]string
	quorum  *corehttp.QuorumResponse
	staged  map[string]string // alias → text ReadStaged returns; default the expected text
	targets map[string]Target
	install map[string]string // sha Install returns; default sha
}

func newOps(targets ...Target) *fakeOps {
	f := &fakeOps{fail: map[string]error{}, active: map[string]string{}, states: map[string][]string{},
		staged: map[string]string{}, targets: map[string]Target{}, install: map[string]string{}}
	for _, t := range targets {
		f.targets[t.Alias] = t
	}
	return f
}

func (f *fakeOps) call(t Target, op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, t.Alias+"/"+op)
	return f.fail[t.Alias+"/"+op]
}

func (f *fakeOps) did(op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, op)
}

func (f *fakeOps) Preflight(_ context.Context, t Target) (remote.Preflight, error) {
	return remote.Preflight{UnitLoad: "loaded", Active: "active", ConfigFSAvailKB: 1 << 20, WorkDirAvailKB: 1 << 30}, f.call(t, "preflight")
}

func (f *fakeOps) GenSeed(_ context.Context, t Target) (string, error) {
	return pkC, f.call(t, "gen-seed")
}

func (f *fakeOps) Stage(_ context.Context, t Target) (remote.Staged, error) {
	if err := f.call(t, "stage"); err != nil {
		return remote.Staged{}, err
	}
	if t.Seed != nil && t.Seed.Mode == stellarcfg.SeedImport && (t.Import == nil || string(t.Import.Bytes()) != seedB) {
		return remote.Staged{}, errors.New("import seed not passed")
	}
	pk := ""
	if t.Seed != nil {
		pk = t.Seed.PublicKey
	}
	return remote.Staged{SHA256: sha, PublicKey: pk}, nil
}

func (f *fakeOps) ReadStaged(_ context.Context, t Target) (remote.RedactedConfig, error) {
	if err := f.call(t, "read-staged"); err != nil {
		return remote.RedactedConfig{}, err
	}
	f.mu.Lock()
	text, ok := f.staged[t.Alias]
	f.mu.Unlock()
	if !ok {
		pk := ""
		if t.Seed != nil {
			pk = t.Seed.PublicKey
		}
		text = string(t.expected(pk))
	}
	return remote.RedactedConfig{Text: []byte(text)}, nil
}

func (f *fakeOps) Backup(_ context.Context, t Target) (string, error) {
	if !t.Running {
		return "", f.call(t, "backup")
	}
	return t.ConfigPath + ".auriga-20261116T120000Z.bak", f.call(t, "backup")
}

func (f *fakeOps) Install(_ context.Context, t Target) (string, error) {
	err := f.call(t, "install")
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.install[t.Alias]; ok {
		return s, err
	}
	if err != nil {
		return "", err
	}
	return sha, nil
}

func (f *fakeOps) Init(_ context.Context, t Target, i Init) error {
	return f.call(t, string(i.Kind))
}

func (f *fakeOps) Restart(_ context.Context, t Target) (string, error) {
	return "active", f.call(t, "restart")
}

func (f *fakeOps) Rollback(_ context.Context, t Target, backup string) (string, error) {
	if err := f.call(t, "rollback "+backup); err != nil {
		return "", err
	}
	if backup == "" {
		return "stopped", f.fail[t.Alias+"/rollback"]
	}
	return "active", f.fail[t.Alias+"/rollback"]
}

func (f *fakeOps) Active(_ context.Context, t Target) (string, error) {
	err := f.call(t, "active")
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.active[t.Alias]; ok {
		return a, err
	}
	return "active", err
}

func (f *fakeOps) Info(_ context.Context, t Target) (*corehttp.Info, error) {
	if err := f.call(t, "info"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	st := "Synced!"
	if q := f.states[t.Alias]; len(q) > 0 {
		st = q[0]
		if len(q) > 1 {
			f.states[t.Alias] = q[1:]
		}
	}
	return &corehttp.Info{State: st, Ledger: corehttp.LedgerInfo{Num: 100, Age: 3}}, nil
}

func (f *fakeOps) Quorum(_ context.Context, t Target) (*corehttp.QuorumResponse, error) {
	if err := f.call(t, "quorum"); err != nil {
		return nil, err
	}
	if f.quorum != nil {
		return f.quorum, nil
	}
	yes, failAt := true, 2
	return &corehttp.QuorumResponse{QSet: &corehttp.QSetStatus{FailAt: &failAt}, Transitive: &corehttp.Transitive{Intersection: &yes}}, nil
}

type memAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
	err     error
}

func (m *memAudit) Append(e audit.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return m.err
}

func (m *memAudit) actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.entries {
		out = append(out, e.Host+" "+e.Action+" "+e.Result)
	}
	return out
}

func target(alias, pk string, mode stellarcfg.SeedMode) Target {
	seed := &stellarcfg.Seed{Mode: mode, PublicKey: pk, Alias: alias}
	r := "# rendered\nNETWORK_PASSPHRASE = \"x\"\nNODE_SEED = \"" + seed.SeedMarker() + "\"\nHTTP_PORT = 11626\n"
	return Target{Alias: alias, ConfigPath: "/etc/stellar/stellar-core.cfg", Service: "stellar-core",
		Rendered: []byte(r), Seed: seed, Running: true}
}

func fast() Options {
	return Options{Org: "acme", Network: "testnet", HealthTimeout: 30 * time.Millisecond, SyncTimeout: time.Second,
		PollEvery: time.Millisecond, StepTimeout: time.Second, InitTimeout: time.Second}
}

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) emit(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) has(host string, step Step, st Status, detail string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Host == host && e.Step == step && e.Status == st && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

func TestHappyRollingDeploy(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-b", pkB, stellarcfg.SeedKeep)}
	ts[1].Inits = []Init{{Kind: remote.InitNewDB}, {Kind: remote.InitNewHist, Archive: "acme-b"}}
	ops, au, rec := newOps(ts...), &memAudit{}, &recorder{}
	ops.states["acme-a"] = []string{"Catching up", "Catching up", "Synced!"}
	c := New(ops, au, ts, fast(), rec.emit)
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := c.Outcomes(); o["acme-a"] != Deployed || o["acme-b"] != Deployed {
		t.Errorf("outcomes %v", o)
	}
	// acme-b starts only after acme-a is synced and checked.
	ia, ib := slices.Index(ops.calls, "acme-a/quorum"), slices.Index(ops.calls, "acme-b/preflight")
	if ia < 0 || ib < ia {
		t.Errorf("not rolling:\n%v", ops.calls)
	}
	want := []string{"acme-a/preflight", "acme-a/stage", "acme-a/read-staged", "acme-a/backup", "acme-a/install", "acme-a/restart"}
	if !slices.Equal(ops.calls[:len(want)], want) {
		t.Errorf("order %v", ops.calls)
	}
	if ib := slices.Index(ops.calls, "acme-b/new-db"); ib < 0 || ops.calls[ib+1] != "acme-b/new-hist" || ops.calls[ib+2] != "acme-b/restart" {
		t.Errorf("inits %v", ops.calls)
	}
	if !rec.has("acme-a", StepSync, Running, "Catching up") || !rec.has("acme-a", StepInit, Skipped, "none planned") {
		t.Error("missing progress events")
	}
	wantAudit := []string{"acme-a deploy.stage ok", "acme-a deploy.backup ok", "acme-a deploy.install ok", "acme-a deploy.restart ok",
		"acme-b deploy.stage ok", "acme-b deploy.backup ok", "acme-b deploy.install ok", "acme-b deploy.new-db ok", "acme-b deploy.new-hist ok", "acme-b deploy.restart ok"}
	if got := au.actions(); !slices.Equal(got, wantAudit) {
		t.Errorf("audit\n%v\nwant\n%v", got, wantAudit)
	}
	for _, e := range au.entries {
		if e.Org != "acme" || e.Network != "testnet" {
			t.Errorf("entry %+v", e)
		}
		if e.Action == "deploy.install" && (e.ConfigSHA256 != sha || !strings.HasSuffix(e.Backup, ".bak")) {
			t.Errorf("install entry %+v", e)
		}
	}
}

// A failure at each step: before install the host is unchanged and nothing
// is rolled back; from install on, the backup is restored. Either way the
// next host is never touched.
func TestFailureAtEachStep(t *testing.T) {
	for _, tc := range []struct {
		op       string
		outcome  Outcome
		rollback bool
	}{
		{"preflight", Unchanged, false},
		{"stage", Unchanged, false},
		{"read-staged", Unchanged, false},
		{"backup", Unchanged, false},
		{"install", Unchanged, false},
		{"new-db", RolledBack, true},
		{"restart", RolledBack, true},
		{"active", RolledBack, true},
		{"info", RolledBack, true},
	} {
		t.Run(tc.op, func(t *testing.T) {
			ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-b", pkB, stellarcfg.SeedKeep)}
			ts[0].Inits = []Init{{Kind: remote.InitNewDB}}
			ops, au, rec := newOps(ts...), &memAudit{}, &recorder{}
			ops.fail["acme-a/"+tc.op] = errBoom
			c := New(ops, au, ts, fast(), rec.emit)
			err := c.Run(context.Background())
			if !errors.Is(err, errBoom) {
				t.Fatalf("err %v", err)
			}
			if o := c.Outcomes(); o["acme-a"] != tc.outcome || o["acme-b"] != NotStarted {
				t.Errorf("outcomes %v", o)
			}
			if rb := ops.did("acme-a/rollback /etc/stellar/stellar-core.cfg.auriga-20261116T120000Z.bak"); rb != tc.rollback {
				t.Errorf("rollback ran: %v\n%v", rb, ops.calls)
			}
			for _, call := range ops.calls {
				if strings.HasPrefix(call, "acme-b/") {
					t.Errorf("next host touched: %v", ops.calls)
				}
			}
			if !rec.has("acme-b", StepPreflight, Skipped, "rollout stopped at acme-a") {
				t.Error("next host not reported as waiting")
			}
			if tc.rollback {
				if !slices.Contains(au.actions(), "acme-a deploy.rollback ok") || !rec.has("acme-a", StepRollback, Done, "restored") {
					t.Errorf("rollback not reported: %v", au.actions())
				}
				if !strings.Contains(err.Error(), "rolled back") {
					t.Errorf("err %v", err)
				}
			}
		})
	}
}

func TestHealthGateTimesOutAndRollsBack(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ops, rec := newOps(ts...), &recorder{}
	ops.active["acme-a"] = "activating"
	c := New(ops, nil, ts, fast(), rec.emit)
	err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not healthy after 30ms: unit stellar-core is activating") || c.Outcomes()["acme-a"] != RolledBack {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
}

func TestRollbackFailureReportsBoth(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ops, au, rec := newOps(ts...), &memAudit{}, &recorder{}
	ops.fail["acme-a/restart"] = errBoom
	ops.fail["acme-a/rollback"] = errors.New("cp failed")
	c := New(ops, au, ts, fast(), rec.emit)
	err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "cp failed") || c.Outcomes()["acme-a"] != RollbackFailed {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
	if !slices.Contains(au.actions(), "acme-a deploy.rollback failed") {
		t.Errorf("audit %v", au.actions())
	}
}

func TestNewNodeRollbackHasNoBackup(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ts[0].Running = false
	ops := newOps(ts...)
	ops.fail["acme-a/restart"] = errBoom
	c := New(ops, nil, ts, fast(), func(Event) {})
	c.Run(context.Background())
	if !ops.did("acme-a/rollback ") || c.Outcomes()["acme-a"] != RolledBack {
		t.Errorf("%v %v", ops.calls, c.Outcomes())
	}
}

func TestVerifyCatchesDifferences(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeOps, *Target)
		want  string
	}{
		{"edited text", func(o *fakeOps, t *Target) {
			o.staged[t.Alias] = strings.Replace(string(t.expected(pkA)), "11626", "11627", 1)
		}, "differs from the reviewed render at line 4"},
		{"other key", func(_ *fakeOps, t *Target) { t.Seed.PublicKey = pkB }, "the staged seed is " + pkB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := target("acme-a", pkA, stellarcfg.SeedKeep)
			ops := newOps(tg)
			tc.setup(ops, &tg)
			if tc.name == "other key" {
				// The review expected pkB; the host's seed derives to pkA.
				ops2 := &stageKey{fakeOps: ops, pk: pkA}
				c := New(ops2, nil, []Target{tg}, fast(), func(Event) {})
				err := c.Run(context.Background())
				if err == nil || !strings.Contains(err.Error(), "the staged seed is "+pkA) || ops.did("acme-a/install") {
					t.Fatalf("%v %v", err, ops.calls)
				}
				return
			}
			c := New(ops, nil, []Target{tg}, fast(), func(Event) {})
			err := c.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) || ops.did("acme-a/backup") {
				t.Fatalf("%v %v", err, ops.calls)
			}
		})
	}
}

type stageKey struct {
	*fakeOps
	pk string
}

func (s *stageKey) Stage(ctx context.Context, t Target) (remote.Staged, error) {
	st, err := s.fakeOps.Stage(ctx, t)
	st.PublicKey = s.pk
	return st, err
}

func TestInstalledFileMustMatchVerified(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ops := newOps(ts...)
	ops.install["acme-a"] = strings.Repeat("1", 64)
	c := New(ops, nil, ts, fast(), func(Event) {})
	err := c.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "installed sha256 111111111111, verified 9f2c00000000") || c.Outcomes()["acme-a"] != RolledBack {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
}

func TestGeneratedKeysReplanBeforeStaging(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-c", "", stellarcfg.SeedGenerate)}
	ops := newOps(ts...)
	var got map[string]string
	opt := fast()
	opt.Replan = func(gen map[string]string) ([]Target, error) {
		got = gen
		return []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-c", gen["acme-c"], stellarcfg.SeedGenerate)}, nil
	}
	au := &memAudit{}
	c := New(ops, au, ts, opt, func(Event) {})
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got["acme-c"] != pkC || ops.calls[0] != "acme-c/gen-seed" || ops.calls[1] != "acme-a/preflight" {
		t.Errorf("%v %v", got, ops.calls)
	}
	if au.entries[0].Action != "deploy.gen-seed" || au.entries[0].PublicKey != pkC {
		t.Errorf("audit %+v", au.entries[0])
	}

	// A re-plan that does not use the generated key stops the deploy.
	opt.Replan = func(map[string]string) ([]Target, error) { return ts, nil }
	ops = newOps(ts...)
	if err := New(ops, nil, ts, opt, func(Event) {}).Run(context.Background()); err == nil || ops.did("acme-a/stage") {
		t.Errorf("bad re-plan accepted: %v", err)
	}
	opt.Replan = func(map[string]string) ([]Target, error) { return nil, errBoom }
	if err := New(newOps(ts...), nil, ts, opt, func(Event) {}).Run(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("re-plan error: %v", err)
	}
	ops = newOps(ts...)
	ops.fail["acme-c/gen-seed"] = errBoom
	if err := New(ops, nil, ts, opt, func(Event) {}).Run(context.Background()); !errors.Is(err, errBoom) || ops.did("acme-a/preflight") {
		t.Errorf("gen-seed failure: %v %v", err, ops.calls)
	}
}

func TestImportSeedIsPassedThenZeroed(t *testing.T) {
	tg := target("acme-a", pkB, stellarcfg.SeedImport)
	buf := secret.FromBytes([]byte(seedB))
	tg.Import = buf
	c := New(newOps(tg), nil, []Target{tg}, fast(), func(Event) {})
	if err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 && strings.Contains(string(buf.Bytes()), "S") {
		t.Error("import seed not zeroed")
	}
	// Close zeroes seeds of hosts never reached.
	tg2 := target("acme-b", pkB, stellarcfg.SeedImport)
	tg2.Import = secret.FromBytes([]byte(seedB))
	ops := newOps(tg, tg2)
	ops.fail["acme-a/preflight"] = errBoom
	tg.Import = secret.FromBytes([]byte(seedB))
	New(ops, nil, []Target{tg, tg2}, fast(), func(Event) {}).Run(context.Background())
	if strings.Contains(string(tg2.Import.Bytes()), "SAAA") {
		t.Error("unreached import seed not zeroed")
	}
}

func TestStopWaitingForSync(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-b", pkB, stellarcfg.SeedKeep)}
	ops, rec := newOps(ts...), &recorder{}
	ops.states["acme-a"] = []string{"Catching up"}
	opt := fast()
	opt.SyncTimeout = time.Minute
	var c *Coordinator
	c = New(ops, nil, ts, opt, func(e Event) {
		rec.emit(e)
		if e.Step == StepSync && e.Status == Running && e.Sync != nil {
			c.StopWaiting(e.Host)
		}
	})
	err := c.Run(context.Background())
	if err == nil || c.Outcomes()["acme-a"] != NotYetSynced || c.Outcomes()["acme-b"] != NotStarted || ops.did("acme-a/rollback") {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
	c.StopWaiting("acme-a") // twice is fine
	if !rec.has("acme-a", StepSync, Skipped, "stopped waiting") {
		t.Error("no event")
	}
}

func TestSyncTimeout(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ops, rec := newOps(ts...), &recorder{}
	ops.states["acme-a"] = []string{"Joining SCP"}
	opt := fast()
	opt.SyncTimeout = 20 * time.Millisecond
	c := New(ops, nil, ts, opt, rec.emit)
	if err := c.Run(context.Background()); err == nil || c.Outcomes()["acme-a"] != NotYetSynced {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
	if !rec.has("acme-a", StepSync, Warned, "not Synced! after 20ms") {
		t.Error("no timeout event")
	}
}

func TestQuorumCheck(t *testing.T) {
	no, yes, one := false, true, 1
	for _, tc := range []struct {
		name    string
		q       *corehttp.QuorumResponse
		fail    bool
		outcome Outcome
		status  Status
		detail  string
	}{
		{"split", &corehttp.QuorumResponse{Transitive: &corehttp.Transitive{Intersection: &no}}, false, DeployedAlert, Failed, "QUORUM INTERSECTION IS FALSE"},
		{"fragile", &corehttp.QuorumResponse{QSet: &corehttp.QSetStatus{FailAt: &one, Missing: corehttp.CountOrList{List: []string{"acme-b"}}},
			Transitive: &corehttp.Transitive{Intersection: &yes}}, false, Deployed, Warned, "fail_at 1: the quorum survives at most one failure; 1 missing: acme-b"},
		{"not computed", &corehttp.QuorumResponse{}, false, Deployed, Warned, "intersection not computed yet"},
		{"error", nil, true, Deployed, Warned, "quorum: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
			ops, rec := newOps(ts...), &recorder{}
			ops.quorum = tc.q
			if tc.fail {
				ops.fail["acme-a/quorum"] = errBoom
			}
			c := New(ops, nil, ts, fast(), rec.emit)
			c.Run(context.Background())
			if c.Outcomes()["acme-a"] != tc.outcome || !rec.has("acme-a", StepQuorum, tc.status, tc.detail) {
				t.Errorf("%v %+v", c.Outcomes(), rec.events)
			}
		})
	}
}

func TestParallel(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep), target("acme-b", pkB, stellarcfg.SeedKeep)}
	ops := newOps(ts...)
	ops.fail["acme-a/stage"] = errBoom
	opt := fast()
	opt.Parallel = true
	c := New(ops, nil, ts, opt, func(Event) {})
	err := c.Run(context.Background())
	if !errors.Is(err, errBoom) || c.Outcomes()["acme-b"] != Deployed || c.Outcomes()["acme-a"] != Unchanged {
		t.Errorf("%v %v", err, c.Outcomes())
	}
	if !AllowParallel("testnet", ts) || AllowParallel("pubnet", ts) {
		t.Error("parallel with running pubnet hosts")
	}
	for i := range ts {
		ts[i].Running = false
	}
	if !AllowParallel("pubnet", ts) {
		t.Error("parallel for new pubnet hosts")
	}
}

func TestPreflightRefusals(t *testing.T) {
	for _, tc := range []struct {
		p    remote.Preflight
		want string
	}{
		{remote.Preflight{UnitLoad: "not-found"}, `unit stellar-core is "not-found"`},
		{remote.Preflight{UnitLoad: "loaded", ConfigFSAvailKB: 5}, "only 5 KB free next to"},
		{remote.Preflight{UnitLoad: "loaded", ConfigFSAvailKB: 1 << 20, WorkDirAvailKB: 2048}, "only 2 MB free where"},
	} {
		ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
		ops := &preflightOps{fakeOps: newOps(ts...), p: tc.p}
		err := New(ops, nil, ts, fast(), func(Event) {}).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), tc.want) || ops.did("acme-a/stage") {
			t.Errorf("%v, want %q", err, tc.want)
		}
	}
}

type preflightOps struct {
	*fakeOps
	p remote.Preflight
}

func (p *preflightOps) Preflight(context.Context, Target) (remote.Preflight, error) { return p.p, nil }

func TestCancelDuringHealthStillRollsBack(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	ops := newOps(ts...)
	ops.active["acme-a"] = "activating"
	ctx, cancel := context.WithCancel(context.Background())
	opt := fast()
	opt.HealthTimeout = time.Minute
	c := New(ops, nil, ts, opt, func(e Event) {
		if e.Step == StepHealth && e.Status == Running {
			go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		}
	})
	err := c.Run(ctx)
	if !errors.Is(err, context.Canceled) || c.Outcomes()["acme-a"] != RolledBack {
		t.Errorf("%v %v", err, c.Outcomes())
	}
}

func TestAuditFailureIsReported(t *testing.T) {
	ts := []Target{target("acme-a", pkA, stellarcfg.SeedKeep)}
	rec := &recorder{}
	c := New(newOps(ts...), &memAudit{err: errors.New("disk full")}, ts, fast(), rec.emit)
	c.Run(context.Background())
	if !rec.has("acme-a", "", Warned, "audit log: disk full") {
		t.Error("audit failure not reported")
	}
}

func TestExpectedRedaction(t *testing.T) {
	tg := target("acme-a", "", stellarcfg.SeedGenerate)
	got := string(tg.expected(pkC))
	if !strings.Contains(got, fmt.Sprintf(`NODE_SEED = "<redacted:%s> acme-a"`, pkC)) || strings.Contains(got, "auriga-seed") {
		t.Errorf("%s", got)
	}
	tg.Seed = nil
	if string(tg.expected("")) != string(tg.Rendered) {
		t.Error("no seed")
	}
}
