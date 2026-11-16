package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

// collector drains pool events like the UI does, records the latest state
// per host and answers prompts with onPrompt.
type collector struct {
	mu       sync.Mutex
	states   map[string]HostState
	history  map[string][]ConnState
	changed  chan struct{}
	onPrompt func(PromptEvent)
}

func collect(t *testing.T, p *Pool, onPrompt func(PromptEvent)) *collector {
	t.Helper()
	c := &collector{states: map[string]HostState{}, history: map[string][]ConnState{}, changed: make(chan struct{}, 1), onPrompt: onPrompt}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range p.Events() {
			switch e := e.(type) {
			case StateEvent:
				c.mu.Lock()
				c.states[e.Alias] = e.HostState
				c.history[e.Alias] = append(c.history[e.Alias], e.State)
				c.mu.Unlock()
				select {
				case c.changed <- struct{}{}:
				default:
				}
			case PromptEvent:
				if c.onPrompt != nil {
					go c.onPrompt(e)
				} else {
					e.Cancel()
				}
			}
		}
	}()
	t.Cleanup(func() {
		p.Close()
		close(p.events)
		<-done
	})
	return c
}

func (c *collector) wait(t *testing.T, alias string, want ConnState, timeout time.Duration) HostState {
	t.Helper()
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		s, ok := c.states[alias]
		c.mu.Unlock()
		if ok && s.State == want {
			return s
		}
		select {
		case <-c.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("%s: state %v (err %q) after %v, want %v", alias, s.State, s.Err, timeout, want)
		}
	}
}

// poolFixture starts n servers that all accept one client key, with their
// host keys in known_hosts.
type poolFixture struct {
	home    string
	key     string
	signer  ssh.Signer
	servers []*sshtest.Server
	hosts   map[string]hostSpec
}

func newPoolFixture(t *testing.T, n int) *poolFixture {
	t.Helper()
	f := &poolFixture{home: t.TempDir(), hosts: map[string]hostSpec{}}
	priv, s := newKey(t)
	f.signer = s
	f.key = filepath.Join(f.home, ".ssh", "id")
	writeKey(t, f.key, priv, "", false)
	for i := range n {
		srv := sshtest.NewServer(t)
		srv.AuthorizedKeys = []ssh.PublicKey{s.PublicKey()}
		srv.Exec = func(cmd string, _ io.Reader, stdout, _ io.Writer) int {
			if cmd == "sleep" {
				time.Sleep(200 * time.Millisecond)
			}
			io.WriteString(stdout, "ok") //nolint:errcheck
			return 0
		}
		srv.Start()
		trustHost(t, f.home, srv)
		f.servers = append(f.servers, srv)
		f.hosts[fmt.Sprintf("h%d", i)] = hostSpec{addr: srv.Addr, identity: []string{f.key}}
	}
	return f
}

func (f *poolFixture) pool(t *testing.T, cfg PoolConfig) *Pool {
	t.Helper()
	cfg.Resolver = fakeResolver(t, f.home, f.hosts)
	if cfg.BackoffMin == 0 {
		cfg.BackoffMin, cfg.BackoffMax = 50*time.Millisecond, 200*time.Millisecond
	}
	return NewPool(cfg)
}

func TestPoolConnectsHostsInParallel(t *testing.T) {
	f := newPoolFixture(t, 3)
	const dialDelay = 400 * time.Millisecond
	p := f.pool(t, PoolConfig{NetDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		time.Sleep(dialDelay)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	c := collect(t, p, nil)
	start := time.Now()
	for alias := range f.hosts {
		p.Add(alias)
	}
	for alias := range f.hosts {
		c.wait(t, alias, StateConnected, 5*time.Second)
	}
	if el := time.Since(start); el > 2*dialDelay {
		t.Errorf("3 hosts took %v; dials are not parallel", el)
	}
	for alias := range f.hosts {
		var out []byte
		err := p.Run(context.Background(), alias, func(ctx context.Context, conn *Conn) error {
			w := &sliceWriter{}
			err := conn.Exec(ctx, "probe", nil, w, io.Discard)
			out = w.b
			return err
		})
		if err != nil || string(out) != "ok" {
			t.Errorf("%s: run %q %v", alias, out, err)
		}
	}
}

type sliceWriter struct{ b []byte }

func (w *sliceWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

func TestPoolTenHosts(t *testing.T) {
	f := newPoolFixture(t, 10)
	p := f.pool(t, PoolConfig{})
	c := collect(t, p, nil)
	for alias := range f.hosts {
		p.Add(alias)
	}
	for alias := range f.hosts {
		c.wait(t, alias, StateConnected, 10*time.Second)
	}
}

func TestPoolChannelCap(t *testing.T) {
	f := newPoolFixture(t, 1)
	p := f.pool(t, PoolConfig{})
	c := collect(t, p, nil)
	p.Add("h0")
	c.wait(t, "h0", StateConnected, 5*time.Second)
	var wg sync.WaitGroup
	var failed atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := p.Run(context.Background(), "h0", func(ctx context.Context, conn *Conn) error {
				return conn.Exec(ctx, "sleep", nil, io.Discard, io.Discard)
			})
			if err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Errorf("%d runs failed", failed.Load())
	}
	if got := f.servers[0].MaxConcurrentChannels(); got > MaxChannelsPerHost || got < 2 {
		t.Errorf("max concurrent channels %d, want 2..%d", got, MaxChannelsPerHost)
	}
}

func TestPoolKeepaliveLossThenRecovery(t *testing.T) {
	f := newPoolFixture(t, 1)
	srv := f.servers[0]
	// Keepalive every second, one miss allowed.
	f.hosts["h0"] = hostSpec{addr: srv.Addr, identity: []string{f.key}}
	p := f.pool(t, PoolConfig{})
	p.cfg.Resolver.env.Run = keepaliveConfig(p.cfg.Resolver.env.Run)
	c := collect(t, p, nil)
	p.Add("h0")
	c.wait(t, "h0", StateConnected, 5*time.Second)

	srv.IgnoreKeepalive.Store(true)
	s := c.wait(t, "h0", StateDisconnected, 6*time.Second)
	if s.Err == "" {
		t.Error("no reason given for the disconnect")
	}
	srv.IgnoreKeepalive.Store(false)
	c.wait(t, "h0", StateConnected, 5*time.Second)
}

// keepaliveConfig appends a 1 s keepalive with one allowed miss to every
// fake ssh -G answer.
func keepaliveConfig(run func(context.Context, string, ...string) ([]byte, error)) func(context.Context, string, ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := run(ctx, name, args...)
		return append(out, "serveraliveinterval 1\nserveralivecountmax 1\n"...), err
	}
}

func TestPoolSSHDStopAndReturn(t *testing.T) {
	f := newPoolFixture(t, 2)
	p := f.pool(t, PoolConfig{})
	c := collect(t, p, nil)
	p.Add("h0")
	p.Add("h1")
	c.wait(t, "h0", StateConnected, 5*time.Second)
	c.wait(t, "h1", StateConnected, 5*time.Second)

	f.servers[0].Stop()
	c.wait(t, "h0", StateDisconnected, 5*time.Second)
	// While h0 is down, h1 keeps working and h0 fails fast.
	if err := p.Run(context.Background(), "h1", func(ctx context.Context, conn *Conn) error {
		return conn.Exec(ctx, "x", nil, io.Discard, io.Discard)
	}); err != nil {
		t.Errorf("h1 affected by h0 outage: %v", err)
	}
	start := time.Now()
	err := p.Run(context.Background(), "h0", func(context.Context, *Conn) error { return nil })
	if !errors.Is(err, ErrNotConnected) || time.Since(start) > time.Second {
		t.Errorf("Run on a down host: %v after %v", err, time.Since(start))
	}

	f.servers[0].Start() // same port
	c.wait(t, "h0", StateConnected, 5*time.Second)
}

func TestPoolAuthFailureWaitsForOperator(t *testing.T) {
	f := newPoolFixture(t, 1)
	srv := f.servers[0]
	srv.AuthorizedKeys = nil
	var dials atomic.Int32
	p := f.pool(t, PoolConfig{NetDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	c := collect(t, p, nil)
	p.Add("h0")
	s := c.wait(t, "h0", StateAuthFailed, 5*time.Second)
	if !s.Waiting || s.Err == "" {
		t.Errorf("state %+v", s)
	}
	time.Sleep(400 * time.Millisecond) // well past BackoffMax
	if n := dials.Load(); n != 1 {
		t.Errorf("retried %d times without the operator", n-1)
	}
	srv.AuthorizedKeys = []ssh.PublicKey{f.signer.PublicKey()}
	p.Reconnect("h0")
	c.wait(t, "h0", StateConnected, 5*time.Second)
}

func TestPoolHostKeyPromptThroughEvents(t *testing.T) {
	f := newPoolFixture(t, 1)
	// A fresh home has no known_hosts; reuse the client key from the fixture.
	home := t.TempDir()
	key := filepath.Join(home, ".ssh", "id")
	writeFile(t, key, string(mustRead(t, f.key)))
	f.home = home
	f.hosts["h0"] = hostSpec{addr: f.servers[0].Addr, identity: []string{key}}
	var asked atomic.Int32
	p := f.pool(t, PoolConfig{})
	c := collect(t, p, func(e PromptEvent) {
		if e.HostKey == nil {
			e.Cancel()
			return
		}
		asked.Add(1)
		e.AnswerHostKey(HostKeyTrustSession)
	})
	p.Add("h0")
	c.wait(t, "h0", StateConnected, 5*time.Second)
	if asked.Load() != 1 {
		t.Errorf("asked %d times", asked.Load())
	}
}

func TestPoolRemoveAndClose(t *testing.T) {
	f := newPoolFixture(t, 1)
	p := f.pool(t, PoolConfig{})
	c := collect(t, p, nil)
	p.Add("h0")
	c.wait(t, "h0", StateConnected, 5*time.Second)
	p.Remove("h0")
	if err := p.Run(context.Background(), "h0", func(context.Context, *Conn) error { return nil }); !errors.Is(err, ErrUnknownHost) {
		t.Errorf("Run after Remove: %v", err)
	}
	if _, ok := p.State("h0"); ok {
		t.Error("state still present")
	}
}

func TestBackoff(t *testing.T) {
	lo, hi := time.Second, 60*time.Second
	for n, base := range map[int]time.Duration{1: 1 * time.Second, 2: 2 * time.Second, 4: 8 * time.Second, 7: 60 * time.Second, 30: 60 * time.Second} {
		for range 50 {
			d := backoff(n, lo, hi)
			if d < base*8/10 || d > base*12/10 {
				t.Fatalf("backoff(%d) = %v, want %v ±20%%", n, d, base)
			}
		}
	}
}
