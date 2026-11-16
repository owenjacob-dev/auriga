//go:build integration

package integration

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// adoptedTarget re-renders a host's running config with Auriga's writer,
// keeping its seed on the host: what a deploy of an adopted, unchanged
// org stages.
func adoptedTarget(t *testing.T, b *app.LiveBackend, alias string) deploy.Target {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rc, err := b.ReadConfig(ctx, alias, remote.DefaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	res, err := stellarcfg.Parse(rc.Text)
	if err != nil {
		t.Fatal(err)
	}
	cfg := res.Config
	if cfg.NodeSeed == nil || cfg.NodeSeed.PublicKey == "" {
		t.Fatalf("%s: no seed in %s", alias, rc.Text)
	}
	out, err := stellarcfg.Render(cfg, stellarcfg.RenderOptions{Version: "itest", Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return deploy.Target{Alias: alias, ConfigPath: remote.DefaultConfigPath, Service: remote.DefaultService,
		Rendered: out, Seed: cfg.NodeSeed, Running: true, HTTPPort: 11626}
}

type eventLog struct {
	mu     sync.Mutex
	events []deploy.Event
}

func (l *eventLog) emit(e deploy.Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) dump(t *testing.T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if e.Status != deploy.Running || e.Sync == nil {
			t.Logf("%-5s %-9s %d %s", e.Host, e.Step, e.Status, e.Detail)
		}
	}
}

func itestOptions() deploy.Options {
	return deploy.Options{Org: "itest", Network: "private", SyncTimeout: 10 * time.Minute, PollEvery: 2 * time.Second}
}

func auditActions(t *testing.T, b *app.LiveBackend) []string {
	entries, bad, err := b.AuditLog().Read()
	if err != nil || bad != 0 {
		t.Fatalf("audit log: %v (%d bad lines)", err, bad)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Host+" "+e.Action+" "+e.Result)
	}
	slices.Reverse(out)
	return out
}

// TestRollingDeployOnThreeHosts deploys Auriga's render of each validator's
// own config, one host at a time, through the real helpers, gates and
// quorum check. it-b needs a sudo password and has no forwarding; it-c is
// behind a jump host.
func TestRollingDeployOnThreeHosts(t *testing.T) {
	b := liveBackend(t)
	var ts []deploy.Target
	for _, a := range targets {
		ts = append(ts, adoptedTarget(t, b, a))
	}
	log := &eventLog{}
	c := deploy.New(b.DeployOps(), b.AuditLog(), ts, itestOptions(), log.emit)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	err := c.Run(ctx)
	log.dump(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range targets {
		if o := c.Outcomes()[a]; o != deploy.Deployed {
			t.Errorf("%s: %s", a, o)
		}
	}
	var want []string
	for _, a := range targets {
		for _, s := range []string{"stage", "backup", "install", "restart"} {
			want = append(want, a+" deploy."+s+" "+audit.OK)
		}
	}
	if got := auditActions(t, b); !slices.Equal(got, want) {
		t.Errorf("audit log\n%v\nwant\n%v", got, want)
	}

	// On each host: the render is in place with the seed, owned root:stellar
	// 0640, with a backup of the previous file.
	for i, a := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		facts, err := b.Probe(ctx, a, remote.DefaultConfigPath, remote.DefaultService)
		if err != nil {
			t.Fatal(err)
		}
		if facts.ConfigOwner != "root:stellar" || facts.ConfigMode != "640" || facts.ServiceActive != "active" {
			t.Errorf("%s: %+v", a, facts)
		}
		rc, err := b.ReadConfig(ctx, a, remote.DefaultConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(string(ts[i].Rendered), `NODE_SEED = "`+ts[i].Seed.SeedMarker()+`"`, `NODE_SEED = "`+ts[i].Seed.Redacted()+`"`, 1)
		if string(rc.Text) != want {
			t.Errorf("%s: installed config differs from the render", a)
		}
		backups, err := b.ListBackups(ctx, a, remote.DefaultConfigPath)
		cancel()
		if err != nil || len(backups) == 0 || backups[0].Owner != "root:stellar" || backups[0].Mode != "640" {
			t.Errorf("%s: backups %+v %v", a, backups, err)
		}
	}
}

// TestForcedBadConfigRollsBack installs a config stellar-core refuses to
// start with. The health gate fails, the backup is restored, the node
// returns to Synced! and the rollout never reaches the next host.
func TestForcedBadConfigRollsBack(t *testing.T) {
	b := liveBackend(t)
	bad := adoptedTarget(t, b, "it-a")
	header, rest, _ := bytes.Cut(bad.Rendered, []byte("\n"))
	bad.Rendered = slices.Concat(header, []byte("\nAURIGA_FORCED_FAILURE = true # not a stellar-core key\n"), rest)
	next := adoptedTarget(t, b, "it-b")

	log := &eventLog{}
	c := deploy.New(b.DeployOps(), b.AuditLog(), []deploy.Target{bad, next}, itestOptions(), log.emit)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := c.Run(ctx)
	log.dump(t)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err %v", err)
	}
	if o := c.Outcomes(); o["it-a"] != deploy.RolledBack || o["it-b"] != deploy.NotStarted {
		t.Fatalf("outcomes %v", o)
	}
	got := auditActions(t, b)
	want := []string{"it-a deploy.stage ok", "it-a deploy.backup ok", "it-a deploy.install ok", "it-a deploy.restart ok", "it-a deploy.rollback ok"}
	if !slices.Equal(got, want) {
		t.Errorf("audit log\n%v\nwant\n%v", got, want)
	}

	// The restored node comes back to Synced! with its previous config.
	client := b.AdminClient("it-a", 11626, remote.DefaultConfigPath, remote.DefaultService)
	deadline := time.Now().Add(5 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r, _, err := client.Info(ctx)
		cancel()
		if err == nil && r.Info.State == "Synced!" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("it-a not Synced! after the rollback: %+v %v", r, err)
		}
		time.Sleep(3 * time.Second)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	rc, err := b.ReadConfig(ctx2, "it-a", remote.DefaultConfigPath)
	if err != nil || bytes.Contains(rc.Text, []byte("AURIGA_FORCED_FAILURE")) {
		t.Errorf("bad config still installed: %v", err)
	}
}

// TestManualRestoreOnRealHost restores it-a's oldest backup (the config
// written before any Auriga deploy) through steps 4 to 10, then checks
// the host runs it and is Synced!.
func TestManualRestoreOnRealHost(t *testing.T) {
	b := liveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	backups, err := b.ListBackups(ctx, "it-a", remote.DefaultConfigPath)
	if err != nil || len(backups) == 0 {
		t.Skipf("no backups on it-a yet (%v); run with the deploy tests", err)
	}
	oldest := backups[len(backups)-1].Path
	bk, err := b.ReadConfig(ctx, "it-a", oldest)
	if err != nil {
		t.Fatal(err)
	}
	tg := deploy.Target{Alias: "it-a", ConfigPath: remote.DefaultConfigPath, Service: remote.DefaultService, HTTPPort: 11626,
		Running: true, RestoreFrom: oldest, Expected: bk.Text}
	log := &eventLog{}
	c := deploy.New(b.DeployOps(), b.AuditLog(), []deploy.Target{tg}, itestOptions(), log.emit)
	err = c.Run(ctx)
	log.dump(t)
	if err != nil || c.Outcomes()["it-a"] != deploy.Deployed {
		t.Fatalf("%v %v", err, c.Outcomes())
	}
	want := []string{"it-a restore.stage ok", "it-a restore.backup ok", "it-a restore.install ok", "it-a restore.restart ok"}
	if got := auditActions(t, b); !slices.Equal(got, want) {
		t.Errorf("audit log %v", got)
	}
	now, err := b.ReadConfig(ctx, "it-a", remote.DefaultConfigPath)
	if err != nil || !bytes.Equal(now.Text, bk.Text) || now.PublicKey != AddressA {
		t.Errorf("it-a does not run the backup: %v", err)
	}
}
