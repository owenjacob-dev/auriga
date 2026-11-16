package sshx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

// ConnState is a host's connection state as shown in the UI (SPEC.md
// "Connection pool").
type ConnState int

const (
	StateConnecting ConnState = iota
	StateConnected
	StateReconnecting
	StateDisconnected
	StateAuthFailed
	StateHostKeyRejected
)

func (s ConnState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	case StateDisconnected:
		return "disconnected"
	case StateAuthFailed:
		return "auth failed"
	case StateHostKeyRejected:
		return "host key rejected"
	}
	return "unknown"
}

// HostState is a snapshot of one host's connection. Only the host's worker
// writes it; the UI receives copies.
type HostState struct {
	Alias     string
	State     ConnState
	Err       string // last error, empty when connected
	Since     time.Time
	Address   string   // resolved host:port
	Route     []string // jump hosts then the alias
	Attempt   int      // consecutive failed attempts
	NextRetry time.Time
	// Waiting is true when the worker will not retry until the operator
	// presses r: wrong keys, host key problems and config errors do not fix
	// themselves.
	Waiting bool
}

// Event is sent from the pool to the UI.
type Event interface{ isEvent() }

// StateEvent reports a changed HostState.
type StateEvent struct{ HostState }

// PromptEvent asks the operator something on behalf of a worker. The UI
// must call exactly one of the answer methods.
type PromptEvent struct {
	Alias   string
	HostKey *HostKeyQuestion // set for host key prompts
	KeyPath string           // set for key passphrase prompts
	Sudo    bool             // set for sudo password prompts
	Retry   bool             // the previous passphrase or password was wrong
	reply   chan promptAnswer
}

type promptAnswer struct {
	decision HostKeyDecision
	pass     *secret.Buffer
	err      error
}

// AnswerHostKey answers a host key prompt.
func (p PromptEvent) AnswerHostKey(d HostKeyDecision) { p.send(promptAnswer{decision: d}) }

// AnswerPassphrase answers a passphrase prompt; the pool takes ownership of
// the buffer.
func (p PromptEvent) AnswerPassphrase(b *secret.Buffer) { p.send(promptAnswer{pass: b}) }

// Cancel declines the prompt.
func (p PromptEvent) Cancel() { p.send(promptAnswer{err: ErrPromptCancelled}) }

func (p PromptEvent) send(a promptAnswer) {
	select {
	case p.reply <- a:
	default: // already answered or the worker gave up
		if a.pass != nil {
			a.pass.Zero()
		}
	}
}

func (StateEvent) isEvent()  {}
func (PromptEvent) isEvent() {}

// ErrPromptCancelled is returned when the operator dismisses a prompt.
var ErrPromptCancelled = errors.New("cancelled by operator")

// ErrNotConnected is returned by Run while a host is not connected.
var ErrNotConnected = errors.New("not connected")

// ErrUnknownHost is returned for an alias not in the pool.
var ErrUnknownHost = errors.New("host not in pool")

// MaxChannelsPerHost caps concurrent channels per host, well under sshd's
// default MaxSessions of 10.
const MaxChannelsPerHost = 4

// PoolConfig configures a Pool.
type PoolConfig struct {
	Resolver  *Resolver
	AgentSock string
	Log       *slog.Logger
	// BackoffMin and BackoffMax bound reconnect delays (default 1 s, 60 s).
	BackoffMin, BackoffMax time.Duration
	// NetDial overrides the first-hop TCP dial (tests).
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Pool holds one worker, and so one SSH connection, per host.
type Pool struct {
	cfg    PoolConfig
	dialer *Dialer
	events chan Event
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	mu      sync.Mutex
	workers map[string]*worker
	wg      sync.WaitGroup
}

// NewPool returns a pool; Add starts hosts.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 60 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		cfg:     cfg,
		events:  make(chan Event, 256),
		ctx:     ctx,
		cancel:  cancel,
		log:     cfg.Log,
		workers: map[string]*worker{},
	}
	p.dialer = &Dialer{
		Resolver:    cfg.Resolver,
		Prompter:    p,
		Trust:       NewTrustStore(),
		Passphrases: NewPassphraseCache(),
		AgentSock:   cfg.AgentSock,
		Log:         cfg.Log,
		NetDial:     cfg.NetDial,
	}
	return p
}

// Events delivers state changes and prompts. The UI must keep reading it.
func (p *Pool) Events() <-chan Event { return p.events }

func (p *Pool) emit(e Event) {
	select {
	case p.events <- e:
	case <-p.ctx.Done():
	}
}

// Add starts a worker for alias (no-op if present).
func (p *Pool) Add(alias string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.workers[alias]; ok || p.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	w := &worker{
		pool: p, alias: alias, ctx: ctx, cancel: cancel,
		requests:  make(chan request),
		reconnect: make(chan struct{}, 1),
		slots:     make(chan struct{}, MaxChannelsPerHost),
		state:     HostState{Alias: alias, State: StateConnecting, Since: time.Now()},
	}
	p.workers[alias] = w
	p.wg.Add(1)
	go w.run()
}

// Remove stops alias's worker and closes its connection. It never touches
// the host.
func (p *Pool) Remove(alias string) {
	p.mu.Lock()
	w := p.workers[alias]
	delete(p.workers, alias)
	p.mu.Unlock()
	if w != nil {
		w.cancel()
	}
}

// Reconnect drops alias's connection (if any) and dials again now,
// including after auth or host key failures.
func (p *Pool) Reconnect(alias string) {
	if w := p.worker(alias); w != nil {
		select {
		case w.reconnect <- struct{}{}:
		default:
		}
	}
}

// State returns a snapshot of alias's state.
func (p *Pool) State(alias string) (HostState, bool) {
	w := p.worker(alias)
	if w == nil {
		return HostState{}, false
	}
	return w.snapshot(), true
}

func (p *Pool) worker(alias string) *worker {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.workers[alias]
}

// Run executes fn with alias's connection, holding one of the host's channel
// slots. fn must open at most one channel at a time. Run fails fast with
// ErrNotConnected when the host is down, and honours ctx.
func (p *Pool) Run(ctx context.Context, alias string, fn func(ctx context.Context, c *Conn) error) error {
	w := p.worker(alias)
	if w == nil {
		return fmt.Errorf("%s: %w", alias, ErrUnknownHost)
	}
	req := request{ctx: ctx, fn: fn, done: make(chan error, 1)}
	select {
	case w.requests <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ctx.Done():
		return fmt.Errorf("%s: %w", alias, ErrUnknownHost)
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops every worker, closes all connections and zeroes cached
// passphrases.
func (p *Pool) Close() {
	p.cancel()
	p.wg.Wait()
	p.dialer.Passphrases.Close()
}

// ConfirmHostKey implements Prompter by asking the UI.
func (p *Pool) ConfirmHostKey(ctx context.Context, q HostKeyQuestion) (HostKeyDecision, error) {
	a, err := p.ask(ctx, PromptEvent{Alias: q.Alias, HostKey: &q})
	if err != nil {
		return HostKeyReject, err
	}
	return a.decision, a.err
}

// Passphrase implements Prompter by asking the UI.
func (p *Pool) Passphrase(ctx context.Context, alias, keyPath string, retry bool) (*secret.Buffer, error) {
	a, err := p.ask(ctx, PromptEvent{Alias: alias, KeyPath: keyPath, Retry: retry})
	if err != nil {
		return nil, err
	}
	if a.err != nil {
		return nil, a.err
	}
	return a.pass, nil
}

// SudoPassword asks the UI for alias's sudo password (remote.PasswordPrompter).
func (p *Pool) SudoPassword(ctx context.Context, alias string, retry bool) (*secret.Buffer, error) {
	a, err := p.ask(ctx, PromptEvent{Alias: alias, Sudo: true, Retry: retry})
	if err != nil {
		return nil, err
	}
	if a.err != nil {
		return nil, a.err
	}
	return a.pass, nil
}

func (p *Pool) ask(ctx context.Context, e PromptEvent) (promptAnswer, error) {
	e.reply = make(chan promptAnswer, 1)
	p.emit(e)
	select {
	case a := <-e.reply:
		return a, nil
	case <-ctx.Done():
		return promptAnswer{}, ctx.Err()
	}
}

type request struct {
	ctx  context.Context
	fn   func(ctx context.Context, c *Conn) error
	done chan error
}

type worker struct {
	pool      *Pool
	alias     string
	ctx       context.Context
	cancel    context.CancelFunc
	requests  chan request
	reconnect chan struct{}
	slots     chan struct{}

	mu    sync.Mutex
	state HostState
	conn  *Conn
}

func (w *worker) snapshot() HostState {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.state
	s.Route = append([]string(nil), s.Route...)
	return s
}

func (w *worker) setState(f func(s *HostState)) {
	w.mu.Lock()
	prev := w.state.State
	f(&w.state)
	if w.state.State != prev {
		w.state.Since = time.Now()
	}
	s := w.state
	s.Route = append([]string(nil), s.Route...)
	w.mu.Unlock()
	w.pool.emit(StateEvent{s})
}

func (w *worker) run() {
	defer w.pool.wg.Done()
	defer w.closeConn()
	log := w.pool.log.With("host", w.alias)
	attempt := 0
	for {
		state := StateConnecting
		if attempt > 0 {
			state = StateReconnecting
		}
		w.setState(func(s *HostState) { s.State, s.NextRetry, s.Waiting = state, time.Time{}, false })

		conn, err := w.pool.dialer.Dial(w.ctx, w.alias)
		if w.ctx.Err() != nil {
			if conn != nil {
				conn.Close() //nolint:errcheck,gosec
			}
			return
		}
		if err != nil {
			attempt++
			st, permanent := classify(err)
			log.Debug("connect failed", "err", err, "attempt", attempt)
			var wait time.Duration
			if !permanent {
				wait = backoff(attempt, w.pool.cfg.BackoffMin, w.pool.cfg.BackoffMax)
			}
			w.setState(func(s *HostState) {
				s.State, s.Err, s.Attempt, s.Waiting = st, err.Error(), attempt, permanent
				if wait > 0 {
					s.NextRetry = time.Now().Add(wait)
				}
			})
			if !w.idle(wait, permanent) {
				return
			}
			if permanent {
				attempt = 0
			}
			continue
		}

		attempt = 0
		w.mu.Lock()
		w.conn = conn
		w.mu.Unlock()
		w.setState(func(s *HostState) {
			s.State, s.Err, s.Attempt = StateConnected, "", 0
			s.Address, s.Route = conn.Config.Address(), conn.Route
		})
		log.Debug("connected", "route", conn.Route)

		reason, forced := w.serve(conn)
		w.closeConn()
		if w.ctx.Err() != nil {
			return
		}
		log.Debug("connection lost", "reason", reason)
		if !forced {
			attempt = 1
			wait := backoff(attempt, w.pool.cfg.BackoffMin, w.pool.cfg.BackoffMax)
			w.setState(func(s *HostState) {
				s.State, s.Err, s.Attempt, s.NextRetry = StateDisconnected, reason, attempt, time.Now().Add(wait)
			})
			if !w.idle(wait, false) {
				return
			}
		}
	}
}

// idle waits before the next dial: for d (transient failures), or until the
// operator forces a reconnect (permanent failures). It returns false when
// the worker is stopping.
func (w *worker) idle(d time.Duration, untilReconnect bool) bool {
	var timer <-chan time.Time
	if !untilReconnect {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	for {
		select {
		case <-w.ctx.Done():
			return false
		case <-w.reconnect:
			return true
		case <-timer:
			return true
		case req := <-w.requests:
			req.done <- fmt.Errorf("%s: %w", w.alias, ErrNotConnected)
		}
	}
}

// serve handles requests until the connection dies, a forced reconnect, or
// shutdown. It returns why it stopped and whether a reconnect was forced.
func (w *worker) serve(conn *Conn) (string, bool) {
	dead := make(chan error, 1)
	go func() { dead <- conn.Wait() }()
	kaFail := make(chan string, 1)
	kaCtx, kaCancel := context.WithCancel(w.ctx)
	defer kaCancel()
	go keepalive(kaCtx, conn.Client, conn.Config.ServerAliveInterval, conn.Config.ServerAliveCountMax, kaFail)

	var inflight sync.WaitGroup
	defer inflight.Wait()
	for {
		select {
		case <-w.ctx.Done():
			return "stopped", false
		case <-w.reconnect:
			return "reconnect requested", true
		case err := <-dead:
			if err == nil {
				return "connection closed", false
			}
			return "connection closed: " + err.Error(), false
		case why := <-kaFail:
			return why, false
		case req := <-w.requests:
			inflight.Add(1)
			go func() {
				defer inflight.Done()
				select {
				case w.slots <- struct{}{}:
				case <-req.ctx.Done():
					req.done <- req.ctx.Err()
					return
				case <-w.ctx.Done():
					req.done <- w.ctx.Err()
					return
				}
				defer func() { <-w.slots }()
				req.done <- req.fn(req.ctx, conn)
			}()
		}
	}
}

func (w *worker) closeConn() {
	w.mu.Lock()
	c := w.conn
	w.conn = nil
	w.mu.Unlock()
	if c != nil {
		c.Close() //nolint:errcheck,gosec
	}
}

// keepalive sends keepalive@openssh.com every interval and reports on fail
// after countMax consecutive unanswered requests.
func keepalive(ctx context.Context, c *ssh.Client, interval time.Duration, countMax int, fail chan<- string) {
	t := time.NewTicker(interval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		reply := make(chan error, 1)
		go func() {
			_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
			reply <- err
		}()
		select {
		case <-ctx.Done():
			return
		case err := <-reply:
			if err == nil {
				misses = 0
				continue
			}
			misses++
		case <-time.After(interval):
			misses++
		}
		if misses >= countMax {
			select {
			case fail <- fmt.Sprintf("no keepalive reply after %d attempts", misses):
			default:
			}
			return
		}
	}
}

// classify maps a dial error to the state shown in the UI and whether the
// worker should wait for the operator instead of retrying.
func classify(err error) (ConnState, bool) {
	var ae *AuthError
	var ce *HostKeyChangedError
	var re *HostKeyRejectedError
	switch {
	case errors.As(err, &ae), errors.Is(err, ErrPromptCancelled):
		return StateAuthFailed, true
	case errors.As(err, &ce), errors.As(err, &re):
		return StateHostKeyRejected, true
	case errors.Is(err, ErrProxyCommand), errors.Is(err, ErrBadAlias):
		return StateDisconnected, true
	}
	return StateDisconnected, false
}

// backoff returns the delay before attempt n (1-based): BackoffMin doubled
// per attempt up to BackoffMax, with ±20% jitter.
func backoff(n int, lo, hi time.Duration) time.Duration {
	d := lo
	for i := 1; i < n && d < hi; i++ {
		d *= 2
	}
	d = min(d, hi)
	jitter := 0.8 + 0.4*rand.Float64() //nolint:gosec // jitter, not security
	return time.Duration(float64(d) * jitter)
}
