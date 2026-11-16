//go:build integration

package integration

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// TestRealConfigsAreAdoptableAndRoundTrip is M3's acceptance on real hosts:
// each validator's config, read with the seed redacted on the host, parses
// into the model, validates cleanly, and survives render → parse with its
// unknown keys kept verbatim.
func TestRealConfigsAreAdoptableAndRoundTrip(t *testing.T) {
	h := NewHome(t, true)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath})})
	t.Cleanup(pool.Close)
	w := watch(t, pool, sshx.HostKeyReject)
	for _, a := range targets {
		pool.Add(a)
	}
	for _, a := range targets {
		w.waitFor(t, a, sshx.StateConnected, 30*time.Second)
	}
	r := remote.NewRunner(pool, pool)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for _, a := range targets {
		t.Run(a, func(t *testing.T) {
			facts, err := r.Probe(ctx, a, remote.DefaultConfigPath, remote.DefaultService)
			if err != nil {
				t.Fatal(err)
			}
			rc, err := r.ReadConfig(ctx, a, remote.DefaultConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			cl := stellarcfg.Classify(true, rc.Text)
			if cl.Class != stellarcfg.Adoptable {
				t.Fatalf("class %v: %v", cl.Class, cl.Reasons)
			}
			cfg := cl.Result.Config
			if cfg.NodeSeed == nil || cfg.NodeSeed.PublicKey != rc.PublicKey || cfg.NodeHomeDomain != "itest.example" || len(cfg.Validators) != 2 {
				t.Errorf("model %+v seed %+v", cfg, cfg.NodeSeed)
			}
			if b := stellarcfg.Blocking(stellarcfg.Validate(cfg, stellarcfg.ValidateOptions{ServiceWorkDir: facts.ServiceWorkDir})); len(b) != 0 {
				t.Errorf("blocking issues on a running validator: %v", b)
			}
			out, err := stellarcfg.Render(cfg, stellarcfg.RenderOptions{Version: "itest", Time: time.Unix(0, 0)})
			if err != nil {
				t.Fatal(err)
			}
			back, err := stellarcfg.Parse(out)
			if err != nil {
				t.Fatalf("render does not parse: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(back.Config, cfg) {
				t.Errorf("model changed after render and parse:\n%+v\n%+v", cfg, back.Config)
			}
			if cfg.UnsafeQuorum == nil || !*cfg.UnsafeQuorum || !strings.Contains(string(out), "UNSAFE_QUORUM = true") {
				t.Errorf("UNSAFE_QUORUM not kept:\n%s", out)
			}
			for _, s := range []string{SeedA, SeedB, SeedC} {
				if strings.Contains(string(rc.Text)+string(out), s) {
					t.Fatal("a seed reached this machine")
				}
			}
		})
	}
}

func TestBackupsListedOnRealHost(t *testing.T) {
	stamp := "20261102T131004Z"
	Compose(t, "exec", "-T", "host-a", "sh", "-c",
		"cp /etc/stellar/stellar-core.cfg /etc/stellar/stellar-core.cfg.auriga-"+stamp+".bak && chown root:stellar /etc/stellar/stellar-core.cfg.auriga-"+stamp+".bak && chmod 640 /etc/stellar/stellar-core.cfg.auriga-"+stamp+".bak")
	t.Cleanup(func() {
		Compose(t, "exec", "-T", "host-a", "rm", "-f", "/etc/stellar/stellar-core.cfg.auriga-"+stamp+".bak")
	})
	h := NewHome(t, true)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath})})
	t.Cleanup(pool.Close)
	w := watch(t, pool, sshx.HostKeyReject)
	pool.Add("it-a")
	w.waitFor(t, "it-a", sshx.StateConnected, 30*time.Second)
	list, err := remote.NewRunner(pool, pool).ListBackups(context.Background(), "it-a", remote.DefaultConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Time != time.Date(2026, 11, 2, 13, 10, 4, 0, time.UTC) || list[0].Owner != "root:stellar" || list[0].Mode != "640" {
		t.Errorf("backups %+v", list)
	}
}

func TestArchiveCheckOnRealHost(t *testing.T) {
	h := NewHome(t, true)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath})})
	t.Cleanup(pool.Close)
	w := watch(t, pool, sshx.HostKeyReject)
	pool.Add("it-a")
	w.waitFor(t, "it-a", sshx.StateConnected, 30*time.Second)
	r := remote.NewRunner(pool, pool)
	defer r.Close()
	// The admin port answers 404 for this path: the archive does not exist.
	c, err := r.CheckArchive(context.Background(), "it-a", "http://127.0.0.1:11626/.well-known/stellar-history.json", []string{"curl", "cp", "aws"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Exists != "no" || len(c.Missing) != 1 || c.Missing[0] != "aws" {
		t.Errorf("%+v", c)
	}
}
