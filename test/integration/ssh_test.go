//go:build integration

package integration

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
)

// watcher drains pool events and answers prompts like an operator.
type watcher struct {
	mu        sync.Mutex
	states    map[string]sshx.HostState
	hostKeyQs []sshx.HostKeyQuestion
	sudoAsked []string
}

func watch(t *testing.T, p *sshx.Pool, hostKey sshx.HostKeyDecision) *watcher {
	w := &watcher{states: map[string]sshx.HostState{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case e := <-p.Events():
				switch e := e.(type) {
				case sshx.StateEvent:
					w.mu.Lock()
					w.states[e.Alias] = e.HostState
					w.mu.Unlock()
				case sshx.PromptEvent:
					w.mu.Lock()
					switch {
					case e.HostKey != nil:
						w.hostKeyQs = append(w.hostKeyQs, *e.HostKey)
						e.AnswerHostKey(hostKey)
					case e.Sudo:
						w.sudoAsked = append(w.sudoAsked, e.Alias)
						e.AnswerPassphrase(secret.FromBytes([]byte(SudoPassword)))
					default:
						e.Cancel()
					}
					w.mu.Unlock()
				}
			}
		}
	}()
	return w
}

func (w *watcher) waitFor(t *testing.T, alias string, want sshx.ConnState, d time.Duration) sshx.HostState {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		w.mu.Lock()
		s := w.states[alias]
		w.mu.Unlock()
		if s.State == want && s.Alias == alias {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: state %v (%s), want %v", alias, s.State, s.Err, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newPool(t *testing.T, h Home) *sshx.Pool {
	env := sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath}
	p := sshx.NewPool(sshx.PoolConfig{Resolver: sshx.NewResolver(env), BackoffMin: 200 * time.Millisecond, BackoffMax: time.Second})
	t.Cleanup(p.Close)
	return p
}

var targets = []string{"it-a", "it-b", "it-c"}

func TestThreeHostsConnectAndProbe(t *testing.T) {
	h := NewHome(t, false)
	p := newPool(t, h)
	w := watch(t, p, sshx.HostKeyTrustAndSave)
	start := time.Now()
	for _, a := range targets {
		p.Add(a)
	}
	for _, a := range targets {
		w.waitFor(t, a, sshx.StateConnected, 30*time.Second)
	}
	t.Logf("three hosts connected in %v", time.Since(start))
	if s, _ := p.State("it-c"); !slices.Equal(s.Route, []string{"it-bastion", "it-c"}) {
		t.Errorf("it-c route %v, want via it-bastion", s.Route)
	}
	// Four keys were unknown: three targets and the bastion.
	if n := len(w.hostKeyQs); n != 4 {
		t.Errorf("%d host key prompts, want 4", n)
	}

	r := remote.NewRunner(p, p)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want := map[string]string{"it-a": AddressA, "it-b": AddressB, "it-c": AddressC}
	var wg sync.WaitGroup
	for _, a := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := r.Probe(ctx, a, remote.DefaultConfigPath, remote.DefaultService)
			if err != nil {
				t.Errorf("%s probe: %v", a, err)
				return
			}
			if !f.StellarUser || !f.CoreInstalled || !strings.HasPrefix(f.CoreVersion, "stellar-core 30.0.0") || f.ServiceActive != "active" ||
				!f.ConfigPresent || f.ConfigOwner != "root:stellar" || f.ConfigMode != "640" || f.HTTPPort != 11626 || f.DiskAvailKB <= 0 {
				t.Errorf("%s probe facts %+v", a, f)
			}
			rc, err := r.ReadConfig(ctx, a, remote.DefaultConfigPath)
			if err != nil {
				t.Errorf("%s read-config: %v", a, err)
				return
			}
			if rc.PublicKey != want[a] || strings.Contains(string(rc.Text), SeedA) || strings.Contains(string(rc.Text), SeedB) || strings.Contains(string(rc.Text), SeedC) {
				t.Errorf("%s: key %s, text:\n%s", a, rc.PublicKey, rc.Text)
			}
		}()
	}
	wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Equal(w.sudoAsked, []string{"it-b"}) {
		t.Errorf("sudo password asked for %v, want only it-b", w.sudoAsked)
	}
}

func TestHostRestartReconnects(t *testing.T) {
	h := NewHome(t, true)
	p := newPool(t, h)
	w := watch(t, p, sshx.HostKeyReject)
	p.Add("it-a")
	p.Add("it-b")
	w.waitFor(t, "it-a", sshx.StateConnected, 30*time.Second)
	w.waitFor(t, "it-b", sshx.StateConnected, 30*time.Second)

	Compose(t, "stop", "-t", "1", "host-a")
	w.waitFor(t, "it-a", sshx.StateDisconnected, 15*time.Second)
	// The other host keeps working during the outage.
	err := p.Run(context.Background(), "it-b", func(ctx context.Context, c *sshx.Conn) error {
		return c.Exec(ctx, "true", nil, nil, nil)
	})
	if err != nil {
		t.Errorf("it-b during it-a outage: %v", err)
	}
	if err := p.Run(context.Background(), "it-a", func(context.Context, *sshx.Conn) error { return nil }); !errors.Is(err, sshx.ErrNotConnected) {
		t.Errorf("it-a while down: %v", err)
	}
	Compose(t, "start", "host-a")
	w.waitFor(t, "it-a", sshx.StateConnected, 30*time.Second)
}

func TestStrictHostKeyCheckingRefuses(t *testing.T) {
	h := NewHome(t, false)
	// Make it-a strict; its key is not known.
	cfg := strings.Replace(mustRead(t, h.ConfigPath), "Host it-a\n", "Host it-a\n  StrictHostKeyChecking yes\n", 1)
	writeFile(t, h.ConfigPath, cfg)
	p := newPool(t, h)
	w := watch(t, p, sshx.HostKeyTrustSession)
	p.Add("it-a")
	s := w.waitFor(t, "it-a", sshx.StateHostKeyRejected, 30*time.Second)
	if !s.Waiting || !strings.Contains(s.Err, "StrictHostKeyChecking") {
		t.Errorf("%+v", s)
	}
	if len(w.hostKeyQs) != 0 {
		t.Error("prompted despite StrictHostKeyChecking yes")
	}
}
