// Package deploy runs a reviewed plan on an org's hosts (SPEC.md "Config
// generation and deployment"): per host, preflight, stage, verify, backup,
// install, one-off init, restart, a health gate with automatic rollback, a
// sync gate and a quorum check; across hosts, one at a time.
//
// The coordinator does no SSH itself: host work goes through Ops, which
// the app implements with the remote helpers and the admin client. It
// reports progress as Events and writes an audit entry for every step that
// changes a host.
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// Init is a one-off initialisation planned for a host.
type Init struct {
	Kind    remote.InitKind
	Archive string // for new-hist
}

// Target is one host's part of the plan.
type Target struct {
	Alias      string
	ConfigPath string
	Service    string
	// Rendered is the reviewed render; its NODE_SEED is a marker.
	Rendered []byte
	// Seed is the render's NODE_SEED (nil when it has none). Its PublicKey
	// is what the staged seed must derive to; for a key generated at
	// deploy time it is filled in by Replan.
	Seed *stellarcfg.Seed
	// Import is the operator's pasted seed for SeedImport. The coordinator
	// zeroes it once staged, and Close zeroes any left.
	Import *secret.Buffer
	Inits  []Init
	// Running is true when the node runs now; new nodes may deploy at once
	// on any network.
	Running bool
	// HTTPPort is the admin port in the render, used by the gates.
	HTTPPort int

	// RestoreFrom makes this a manual rollback (SPEC.md "Manual
	// rollback"): the backup at this path is staged on the host, seed and
	// all, instead of a render. Expected is that backup as read-config
	// returns it, with the seed redacted; the staged file must match it.
	RestoreFrom string
	Expected    []byte
}

// SeedSourceOf is where the stage helper takes t's seed from.
func SeedSourceOf(t Target) remote.SeedSource { return t.source() }

func (t Target) source() remote.SeedSource {
	if t.Seed == nil {
		return remote.SeedNone
	}
	switch t.Seed.Mode {
	case stellarcfg.SeedGenerate:
		return remote.SeedFromPending
	case stellarcfg.SeedImport:
		return remote.SeedFromStdin
	}
	return remote.SeedFromConfig
}

// expected is the render as read-config returns the staged file: the seed
// marker replaced by the redacted form with pk.
func (t Target) expected(pk string) []byte {
	if t.RestoreFrom != "" {
		return t.Expected
	}
	if t.Seed == nil {
		return t.Rendered
	}
	s := *t.Seed
	marker := `NODE_SEED = "` + s.SeedMarker() + `"`
	s.PublicKey = pk
	return bytes.Replace(t.Rendered, []byte(marker), []byte(`NODE_SEED = "`+s.Redacted()+`"`), 1)
}

// Ops is the host work the coordinator needs. Every call is for one host
// and carries a context with a timeout.
type Ops interface {
	Preflight(ctx context.Context, t Target) (remote.Preflight, error)
	GenSeed(ctx context.Context, t Target) (string, error)
	Stage(ctx context.Context, t Target) (remote.Staged, error)
	// StageBackup stages t.RestoreFrom and returns the staged sha256.
	StageBackup(ctx context.Context, t Target) (string, error)
	// ReadStaged reads the staged file with its seed redacted on the host.
	ReadStaged(ctx context.Context, t Target) (remote.RedactedConfig, error)
	Backup(ctx context.Context, t Target) (string, error)
	Install(ctx context.Context, t Target) (string, error)
	Init(ctx context.Context, t Target, i Init) error
	Restart(ctx context.Context, t Target) (string, error)
	Rollback(ctx context.Context, t Target, backup string) (string, error)
	// Active is the unit's state ("active", "activating", "failed", …).
	Active(ctx context.Context, t Target) (string, error)
	Info(ctx context.Context, t Target) (*corehttp.Info, error)
	Quorum(ctx context.Context, t Target) (*corehttp.QuorumResponse, error)
}

// Auditor records mutating steps; *audit.Log implements it.
type Auditor interface {
	Append(audit.Entry) error
}

// Step is one stage of a host's deploy.
type Step string

const (
	StepKeys      Step = "keys"
	StepPreflight Step = "preflight"
	StepStage     Step = "stage"
	StepVerify    Step = "verify"
	StepBackup    Step = "backup"
	StepInstall   Step = "install"
	StepInit      Step = "init"
	StepRestart   Step = "restart"
	StepHealth    Step = "health"
	StepSync      Step = "sync"
	StepQuorum    Step = "quorum"
	StepRollback  Step = "rollback"
)

// Steps is the order shown on the progress screen.
var Steps = []Step{StepKeys, StepPreflight, StepStage, StepVerify, StepBackup, StepInstall, StepInit, StepRestart, StepHealth, StepSync, StepQuorum, StepRollback}

// Status is a step's state.
type Status int

const (
	Pending Status = iota
	Running
	Done
	Warned
	Failed
	Skipped
)

// Outcome is how a host's deploy ended.
type Outcome string

const (
	NotStarted     Outcome = "not started"
	Unchanged      Outcome = "failed before install; host unchanged"
	Deployed       Outcome = "deployed"
	NotYetSynced   Outcome = "deployed, not yet synced"
	DeployedAlert  Outcome = "deployed; quorum check failed"
	RolledBack     Outcome = "failed; rolled back"
	RollbackFailed Outcome = "failed; rollback failed"
	Cancelled      Outcome = "cancelled"
)

// Sync is live progress during the sync gate.
type Sync struct {
	State  string
	Ledger uint64
	Age    int64
}

// Event is one progress report. Host is "" for the whole deploy.
type Event struct {
	Host   string
	Step   Step
	Status Status
	Detail string
	Sync   *Sync
	// Outcome is set on a host's last event.
	Outcome Outcome
}

// Options are the deploy's settings.
type Options struct {
	Org, Network string
	// Parallel deploys all hosts at once (testnet, or hosts not running).
	Parallel bool
	// Replan rebuilds the targets with the public keys of seeds generated
	// on hosts, before anything is staged.
	Replan func(generated map[string]string) ([]Target, error)

	HealthTimeout time.Duration // default 60 s
	SyncTimeout   time.Duration // default 30 min
	PollEvery     time.Duration // default 2 s
	StepTimeout   time.Duration // per helper call; default 2 min
	InitTimeout   time.Duration // new-db and new-hist; default 15 min
	// MinConfigFSKB and MinWorkDirKB are the preflight's free-space floors.
	MinConfigFSKB, MinWorkDirKB int64
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&o.HealthTimeout, 60*time.Second)
	set(&o.SyncTimeout, 30*time.Minute)
	set(&o.PollEvery, 2*time.Second)
	set(&o.StepTimeout, 2*time.Minute)
	set(&o.InitTimeout, 15*time.Minute)
	if o.MinConfigFSKB == 0 {
		o.MinConfigFSKB = 10 * 1024
	}
	if o.MinWorkDirKB == 0 {
		o.MinWorkDirKB = 1024 * 1024
	}
}

// AllowParallel reports whether "all at once" may be offered: on testnet,
// or when no target is running.
func AllowParallel(network string, targets []Target) bool {
	if network == "testnet" {
		return true
	}
	for _, t := range targets {
		if t.Running {
			return false
		}
	}
	return true
}

// Coordinator runs one deploy.
type Coordinator struct {
	ops     Ops
	auditor Auditor
	opt     Options
	targets []Target
	emit    func(Event)

	mu       sync.Mutex
	skip     map[string]chan struct{}
	outcomes map[string]Outcome
}

// New prepares a deploy of targets in order. emit receives every event, from
// the coordinator's goroutines; it must not block for long.
func New(ops Ops, auditor Auditor, targets []Target, opt Options, emit func(Event)) *Coordinator {
	opt.defaults()
	c := &Coordinator{ops: ops, auditor: auditor, opt: opt, targets: targets, emit: emit,
		skip: map[string]chan struct{}{}, outcomes: map[string]Outcome{}}
	for _, t := range targets {
		c.skip[t.Alias] = make(chan struct{})
		c.outcomes[t.Alias] = NotStarted
	}
	return c
}

// StopWaiting ends a host's sync gate; the host is marked "deployed, not
// yet synced" and the rollout stops there.
func (c *Coordinator) StopWaiting(alias string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.skip[alias]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// Close zeroes any import seed not yet staged.
func (c *Coordinator) Close() {
	for _, t := range c.targets {
		t.Import.Zero()
	}
}

// Outcomes returns each host's outcome.
func (c *Coordinator) Outcomes() map[string]Outcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Outcome, len(c.outcomes))
	for k, v := range c.outcomes {
		out[k] = v
	}
	return out
}

func (c *Coordinator) setOutcome(alias string, o Outcome) {
	c.mu.Lock()
	c.outcomes[alias] = o
	c.mu.Unlock()
}

// Run deploys and returns when every host has finished or the rollout has
// stopped. It returns nil when every host deployed.
func (c *Coordinator) Run(ctx context.Context) error {
	defer c.Close()
	if err := c.keys(ctx); err != nil {
		return err
	}
	if c.opt.Parallel {
		var wg sync.WaitGroup
		errs := make([]error, len(c.targets))
		for i, t := range c.targets {
			wg.Go(func() { errs[i] = c.host(ctx, t) })
		}
		wg.Wait()
		return errors.Join(errs...)
	}
	for i, t := range c.targets {
		if err := c.host(ctx, t); err != nil {
			for _, rest := range c.targets[i+1:] {
				c.emit(Event{Host: rest.Alias, Step: StepPreflight, Status: Skipped, Detail: "rollout stopped at " + t.Alias, Outcome: NotStarted})
			}
			return err
		}
	}
	return nil
}

// keys generates seeds on the hosts that need one and re-plans with their
// public keys, so every render names the real keys before staging.
func (c *Coordinator) keys(ctx context.Context) error {
	generated := map[string]string{}
	for _, t := range c.targets {
		if t.source() != remote.SeedFromPending {
			continue
		}
		c.emit(Event{Host: t.Alias, Step: StepKeys, Status: Running, Detail: "stellar-core gen-seed on the host"})
		sctx, cancel := context.WithTimeout(ctx, c.opt.StepTimeout)
		pk, err := c.ops.GenSeed(sctx, t)
		cancel()
		c.audit(t, "deploy.gen-seed", audit.Entry{PublicKey: pk}, err)
		if err != nil {
			c.emit(Event{Host: t.Alias, Step: StepKeys, Status: Failed, Detail: err.Error(), Outcome: Unchanged})
			c.setOutcome(t.Alias, Unchanged)
			return fmt.Errorf("%s: gen-seed: %w", t.Alias, err)
		}
		generated[t.Alias] = pk
		c.emit(Event{Host: t.Alias, Step: StepKeys, Status: Done, Detail: "new key " + pk})
	}
	if len(generated) == 0 {
		return nil
	}
	if c.opt.Replan == nil {
		return errors.New("keys were generated but the deploy cannot re-plan")
	}
	targets, err := c.opt.Replan(generated)
	if err != nil {
		c.emit(Event{Step: StepKeys, Status: Failed, Detail: "re-plan with the new keys: " + err.Error()})
		return fmt.Errorf("re-plan: %w", err)
	}
	if len(targets) != len(c.targets) {
		return errors.New("re-plan changed the hosts")
	}
	for i := range targets {
		if targets[i].Alias != c.targets[i].Alias {
			return errors.New("re-plan changed the hosts")
		}
		if pk := generated[targets[i].Alias]; pk != "" && (targets[i].Seed == nil || targets[i].Seed.PublicKey != pk) {
			return fmt.Errorf("%s: re-plan does not use the generated key", targets[i].Alias)
		}
		targets[i].Import = c.targets[i].Import
	}
	c.targets = targets
	return nil
}

// failure is a step's error.
type failure struct {
	step Step
	err  error
}

func (f *failure) Error() string { return fmt.Sprintf("%s: %v", f.step, f.err) }
func (f *failure) Unwrap() error { return f.err }

func (c *Coordinator) host(ctx context.Context, t Target) error {
	run := func(step Step, timeout time.Duration, fn func(context.Context) (string, error)) error {
		c.emit(Event{Host: t.Alias, Step: step, Status: Running})
		sctx, cancel := context.WithTimeout(ctx, timeout)
		detail, err := fn(sctx)
		cancel()
		if err != nil {
			c.emit(Event{Host: t.Alias, Step: step, Status: Failed, Detail: err.Error()})
			return &failure{step, err}
		}
		c.emit(Event{Host: t.Alias, Step: step, Status: Done, Detail: detail})
		return nil
	}
	finish := func(o Outcome, detail string) {
		c.setOutcome(t.Alias, o)
		c.emit(Event{Host: t.Alias, Status: Done, Detail: detail, Outcome: o})
	}
	st := c.opt.StepTimeout

	// Before install nothing on the host changes but the staged file.
	var staged remote.Staged
	err := run(StepPreflight, st, func(ctx context.Context) (string, error) {
		p, err := c.ops.Preflight(ctx, t)
		if err != nil {
			return "", err
		}
		switch {
		case p.UnitLoad != "loaded":
			return "", fmt.Errorf("systemd unit %s is %q; install the stellar-core package first", t.Service, p.UnitLoad)
		case p.ConfigFSAvailKB < c.opt.MinConfigFSKB:
			return "", fmt.Errorf("only %d KB free next to %s", p.ConfigFSAvailKB, t.ConfigPath)
		case p.WorkDirAvailKB < c.opt.MinWorkDirKB:
			return "", fmt.Errorf("only %d MB free where %s runs", p.WorkDirAvailKB/1024, t.Service)
		}
		return "unit " + p.Active, nil
	})
	if err == nil {
		err = run(StepStage, st, func(ctx context.Context) (string, error) {
			if t.RestoreFrom != "" {
				sum, err := c.ops.StageBackup(ctx, t)
				staged.SHA256 = sum
				return "copied " + t.RestoreFrom, err
			}
			s, err := c.ops.Stage(ctx, t)
			if err != nil {
				return "", err
			}
			t.Import.Zero()
			staged = s
			return "sha256 " + short(s.SHA256), nil
		})
		c.audit(t, c.action(t, "stage"), audit.Entry{ConfigSHA256: staged.SHA256, PublicKey: staged.PublicKey, Backup: t.RestoreFrom}, err)
	}
	if err == nil {
		err = run(StepVerify, st, func(ctx context.Context) (string, error) {
			if t.RestoreFrom == "" && t.Seed != nil && t.Seed.PublicKey != "" && staged.PublicKey != t.Seed.PublicKey {
				return "", fmt.Errorf("the staged seed is %s, the review expected %s", staged.PublicKey, t.Seed.PublicKey)
			}
			rc, err := c.ops.ReadStaged(ctx, t)
			if err != nil {
				return "", err
			}
			if want := t.expected(staged.PublicKey); !bytes.Equal(rc.Text, want) {
				return "", fmt.Errorf("the staged file differs from what was reviewed at line %d", firstDiffLine(rc.Text, want))
			}
			return "matches the review", nil
		})
	}
	var backup string
	if err == nil {
		err = run(StepBackup, st, func(ctx context.Context) (string, error) {
			b, err := c.ops.Backup(ctx, t)
			backup = b
			if b == "" && err == nil {
				return "no config yet", nil
			}
			return b, err
		})
		c.audit(t, c.action(t, "backup"), audit.Entry{Backup: backup}, err)
	}
	if err != nil {
		finish(Unchanged, err.Error())
		return fmt.Errorf("%s: %w", t.Alias, err)
	}
	var installed string
	err = run(StepInstall, st, func(ctx context.Context) (string, error) {
		sum, err := c.ops.Install(ctx, t)
		installed = sum
		if err == nil && sum != staged.SHA256 {
			// The file changed between verify and install.
			err = fmt.Errorf("installed sha256 %s, verified %s", short(sum), short(staged.SHA256))
		}
		return "sha256 " + short(sum), err
	})
	c.audit(t, c.action(t, "install"), audit.Entry{ConfigSHA256: installed, Backup: backup}, err)
	if err != nil {
		if installed == "" {
			// The rename did not happen.
			finish(Unchanged, err.Error())
			return fmt.Errorf("%s: %w", t.Alias, err)
		}
		return c.rollback(ctx, t, backup, err, finish)
	}

	// Installed: from here a failure restores the backup.
	for _, in := range t.Inits {
		name := string(in.Kind)
		if in.Archive != "" {
			name += " " + in.Archive
		}
		err = run(StepInit, c.opt.InitTimeout, func(ctx context.Context) (string, error) {
			return name, c.ops.Init(ctx, t, in)
		})
		c.audit(t, c.action(t, string(in.Kind)), audit.Entry{Detail: in.Archive}, err)
		if err != nil {
			return c.rollback(ctx, t, backup, err, finish)
		}
	}
	if len(t.Inits) == 0 {
		c.emit(Event{Host: t.Alias, Step: StepInit, Status: Skipped, Detail: "none planned"})
	}
	err = run(StepRestart, st, func(ctx context.Context) (string, error) {
		a, err := c.ops.Restart(ctx, t)
		return "unit " + a, err
	})
	c.audit(t, c.action(t, "restart"), audit.Entry{}, err)
	if err == nil {
		err = run(StepHealth, c.opt.HealthTimeout+st, func(ctx context.Context) (string, error) { return c.health(ctx, t) })
	}
	if err != nil {
		return c.rollback(ctx, t, backup, err, finish)
	}

	synced, err := c.syncGate(ctx, t)
	if err != nil {
		// Cancelled while waiting: the config is in place and healthy.
		finish(NotYetSynced, err.Error())
		return err
	}
	if !synced {
		finish(NotYetSynced, "stopped waiting for Synced!; the rollout stops here")
		return fmt.Errorf("%s: deployed but not yet synced", t.Alias)
	}
	if err := c.quorumCheck(ctx, t); err != nil {
		finish(DeployedAlert, err.Error())
		return err
	}
	finish(Deployed, "synced")
	return nil
}

// rollback restores the backup after a failure past install and reports
// both the failure and the rollback result.
func (c *Coordinator) rollback(ctx context.Context, t Target, backup string, cause error, finish func(Outcome, string)) error {
	c.emit(Event{Host: t.Alias, Step: StepRollback, Status: Running, Detail: "restoring " + orNew(backup)})
	// The deploy's context may be cancelled; the rollback still runs.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.opt.StepTimeout)
	defer cancel()
	active, err := c.ops.Rollback(rctx, t, backup)
	if err == nil && backup != "" && active != "active" {
		err = fmt.Errorf("the unit is %q after the restore", active)
	}
	c.audit(t, c.action(t, "rollback"), audit.Entry{Backup: backup, Detail: cause.Error()}, err)
	if err != nil {
		c.emit(Event{Host: t.Alias, Step: StepRollback, Status: Failed, Detail: err.Error()})
		finish(RollbackFailed, fmt.Sprintf("%v; rollback: %v", cause, err))
		return fmt.Errorf("%s: %w; rollback failed: %w", t.Alias, cause, err)
	}
	c.emit(Event{Host: t.Alias, Step: StepRollback, Status: Done, Detail: "restored " + orNew(backup) + ", unit " + active})
	finish(RolledBack, cause.Error())
	return fmt.Errorf("%s: %w; rolled back", t.Alias, cause)
}

func orNew(backup string) string {
	if backup == "" {
		return "nothing (new node; config moved aside, service stopped)"
	}
	return backup
}

// health waits until the unit is active and info answers.
func (c *Coordinator) health(ctx context.Context, t Target) (string, error) {
	deadline := time.Now().Add(c.opt.HealthTimeout)
	var last error
	for {
		a, err := c.ops.Active(ctx, t)
		switch {
		case err != nil:
			last = err
		case a != "active":
			last = fmt.Errorf("unit %s is %s", t.Service, a)
		default:
			info, err := c.ops.Info(ctx, t)
			if err == nil {
				return "info answers: " + info.State, nil
			}
			last = fmt.Errorf("info: %w", err)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("not healthy after %s: %w", c.opt.HealthTimeout, last)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(c.opt.PollEvery):
		}
	}
}

// syncGate waits for Synced!. It returns false when the operator stopped
// waiting or the timeout passed.
func (c *Coordinator) syncGate(ctx context.Context, t Target) (bool, error) {
	c.emit(Event{Host: t.Alias, Step: StepSync, Status: Running, Detail: "waiting for Synced!"})
	deadline := time.After(c.opt.SyncTimeout)
	skip := c.skip[t.Alias]
	for {
		qctx, cancel := context.WithTimeout(ctx, c.opt.StepTimeout)
		info, err := c.ops.Info(qctx, t)
		cancel()
		if err == nil {
			s := &Sync{State: info.State, Ledger: info.Ledger.Num, Age: info.Ledger.Age}
			if info.State == "Synced!" {
				c.emit(Event{Host: t.Alias, Step: StepSync, Status: Done, Detail: fmt.Sprintf("Synced! at ledger %d", s.Ledger), Sync: s})
				return true, nil
			}
			c.emit(Event{Host: t.Alias, Step: StepSync, Status: Running, Detail: info.State, Sync: s})
		} else {
			c.emit(Event{Host: t.Alias, Step: StepSync, Status: Running, Detail: "info: " + err.Error()})
		}
		select {
		case <-ctx.Done():
			c.emit(Event{Host: t.Alias, Step: StepSync, Status: Skipped, Detail: "cancelled"})
			return false, ctx.Err()
		case <-skip:
			c.emit(Event{Host: t.Alias, Step: StepSync, Status: Skipped, Detail: "stopped waiting"})
			return false, nil
		case <-deadline:
			c.emit(Event{Host: t.Alias, Step: StepSync, Status: Warned, Detail: fmt.Sprintf("not Synced! after %s", c.opt.SyncTimeout)})
			return false, nil
		case <-time.After(c.opt.PollEvery):
		}
	}
}

// quorumCheck applies SPEC.md step 10: intersection must hold; fail_at of
// 1 or less and missing nodes are warnings.
func (c *Coordinator) quorumCheck(ctx context.Context, t Target) error {
	c.emit(Event{Host: t.Alias, Step: StepQuorum, Status: Running})
	qctx, cancel := context.WithTimeout(ctx, c.opt.StepTimeout)
	defer cancel()
	q, err := c.ops.Quorum(qctx, t)
	if err != nil {
		c.emit(Event{Host: t.Alias, Step: StepQuorum, Status: Warned, Detail: "quorum: " + err.Error()})
		return nil
	}
	if q.Transitive != nil && q.Transitive.Intersection != nil && !*q.Transitive.Intersection {
		err := errors.New("QUORUM INTERSECTION IS FALSE: the network can split; the rollout stops")
		c.emit(Event{Host: t.Alias, Step: StepQuorum, Status: Failed, Detail: err.Error()})
		return fmt.Errorf("%s: %w", t.Alias, err)
	}
	var warns []string
	if q.Transitive == nil || q.Transitive.Intersection == nil {
		warns = append(warns, "intersection not computed yet")
	}
	if q.QSet != nil {
		if q.QSet.FailAt != nil && *q.QSet.FailAt <= 1 {
			warns = append(warns, fmt.Sprintf("fail_at %d: the quorum survives at most one failure", *q.QSet.FailAt))
		}
		if q.QSet.Missing.N() > 0 {
			m := fmt.Sprintf("%d missing", q.QSet.Missing.N())
			if len(q.QSet.Missing.List) > 0 {
				m += ": " + strings.Join(q.QSet.Missing.List, ", ")
			}
			warns = append(warns, m)
		}
	}
	if len(warns) > 0 {
		c.emit(Event{Host: t.Alias, Step: StepQuorum, Status: Warned, Detail: strings.Join(warns, "; ")})
		return nil
	}
	c.emit(Event{Host: t.Alias, Step: StepQuorum, Status: Done, Detail: "intersection ok"})
	return nil
}

// action names an audit action: "deploy.<step>", or "restore.<step>" for
// a manual rollback.
func (c *Coordinator) action(t Target, step string) string {
	if t.RestoreFrom != "" {
		return "restore." + step
	}
	return "deploy." + step
}

func (c *Coordinator) audit(t Target, action string, e audit.Entry, err error) {
	if c.auditor == nil {
		return
	}
	e.Host, e.Org, e.Network, e.Action, e.Result = t.Alias, c.opt.Org, c.opt.Network, action, audit.OK
	if err != nil {
		e.Result, e.Error = audit.Failed, err.Error()
	}
	if aerr := c.auditor.Append(e); aerr != nil {
		c.emit(Event{Host: t.Alias, Status: Warned, Detail: "audit log: " + aerr.Error()})
	}
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

func firstDiffLine(a, b []byte) int {
	al, bl := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	for i := range max(len(al), len(bl)) {
		if i >= len(al) || i >= len(bl) || !bytes.Equal(al[i], bl[i]) {
			return i + 1
		}
	}
	return 0
}
