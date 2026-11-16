//go:build integration

package integration

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
)

// liveBackend runs app.LiveBackend against the stack, answering prompts
// (sudo password for it-b) like an operator.
func liveBackend(t *testing.T) *app.LiveBackend {
	h := NewHome(t, true)
	b := app.NewLiveBackend(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath}, "", t.TempDir(), slog.New(slog.DiscardHandler))
	t.Cleanup(b.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	states := map[string]sshx.ConnState{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-b.Events():
				switch e := e.(type) {
				case sshx.StateEvent:
					mu.Lock()
					states[e.Alias] = e.State
					mu.Unlock()
				case sshx.PromptEvent:
					if e.Sudo {
						e.AnswerPassphrase(secret.FromBytes([]byte(SudoPassword)))
					} else {
						e.Cancel()
					}
				}
			}
		}
	}()
	for _, a := range targets {
		b.Connect(a)
	}
	deadline := time.Now().Add(30 * time.Second)
	for _, a := range targets {
		for {
			mu.Lock()
			s := states[a]
			mu.Unlock()
			if s == sshx.StateConnected {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s not connected (%v)", a, s)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return b
}

// TestLiveInfoOnThreeHosts is M2's acceptance: live info from all three
// validators, over the SSH tunnel (it-a, and it-c through the bastion) and
// over the fallback on it-b, whose sshd refuses forwarding and which has no
// curl, so the fallback uses stellar-core http-command.
func TestLiveInfoOnThreeHosts(t *testing.T) {
	b := liveBackend(t)
	wantTransport := map[string]string{"it-a": "ssh tunnel", "it-b": "on host (http-command)", "it-c": "ssh tunnel"}
	var ledgers []uint64
	for _, a := range targets {
		c := b.AdminClient(a, 11626, remote.DefaultConfigPath, remote.DefaultService)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r, raw, err := c.Info(ctx)
		cancel()
		if err != nil {
			t.Fatalf("%s info: %v", a, err)
		}
		i := r.Info
		if i.Network != "Auriga Integration Network ; November 2026" || !strings.HasPrefix(i.Build, "stellar-core 30.0.0") || i.Ledger.Num == 0 || len(raw) == 0 {
			t.Errorf("%s: %+v", a, i)
		}
		if got := c.Transport().Name(); got != wantTransport[a] {
			t.Errorf("%s transport %q, want %q", a, got, wantTransport[a])
		}
		q, _, err := c.Quorum(context.Background(), false)
		if err != nil || q.QSet == nil || q.QSet.Agree.N() != 3 {
			t.Errorf("%s quorum: %+v %v", a, q, err)
		}
		ledgers = append(ledgers, i.Ledger.Num)
		t.Logf("%s: %s ledger %d via %s", a, i.State, i.Ledger.Num, c.Transport().Name())
	}
	// All three are on the same network and close ledgers together.
	for _, l := range ledgers {
		if diff := int64(l) - int64(ledgers[0]); diff > 5 || diff < -5 {
			t.Errorf("ledgers diverge: %v", ledgers)
		}
	}
}

// TestAdminAllowlistOnHost checks that the fallback helper refuses anything
// outside the six read-only commands before it reaches the host.
func TestAdminAllowlistOnHost(t *testing.T) {
	h := NewHome(t, true)
	pool := sshx.NewPool(sshx.PoolConfig{Resolver: sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath})})
	t.Cleanup(pool.Close)
	r := remote.NewRunner(pool, nil)
	for _, cmd := range []string{"manualclose", "tx?blob=AAAA", "upgrades?mode=set&upgradetime=2026-01-01T00:00:00Z"} {
		if _, err := r.AdminGet(context.Background(), "it-a", 11626, cmd, remote.DefaultConfigPath, remote.DefaultService); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: %v", cmd, err)
		}
	}
}
