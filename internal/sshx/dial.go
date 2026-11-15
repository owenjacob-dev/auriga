package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

// maxJumpDepth bounds ProxyJump recursion (a hop whose own config has a
// ProxyJump, and so on), guarding against loops.
const maxJumpDepth = 8

// Dialer opens SSH connections the way OpenSSH would for an alias.
type Dialer struct {
	Resolver    *Resolver
	Prompter    Prompter
	Trust       *TrustStore
	Passphrases *PassphraseCache
	// AgentSock is SSH_AUTH_SOCK; empty disables the agent.
	AgentSock string
	Log       *slog.Logger
	// NetDial opens the first TCP connection; nil uses net.Dialer.
	NetDial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Conn is an open connection to a host, possibly through jump hosts.
type Conn struct {
	*ssh.Client
	Config HostConfig
	// Route lists the hops in order, ending with the target, for display.
	Route []string
	hops  []*ssh.Client
}

// Close closes the target connection and every jump connection.
func (c *Conn) Close() error {
	err := c.Client.Close()
	for i := len(c.hops) - 1; i >= 0; i-- {
		c.hops[i].Close() //nolint:errcheck,gosec
	}
	return err
}

// Dial resolves alias and connects to it.
func (d *Dialer) Dial(ctx context.Context, alias string) (*Conn, error) {
	h, err := d.Resolver.Resolve(ctx, alias)
	if err != nil {
		return nil, err
	}
	conn := &Conn{Config: h}
	client, err := d.dialHost(ctx, alias, h, nil, conn, 0)
	if err != nil {
		for i := len(conn.hops) - 1; i >= 0; i-- {
			conn.hops[i].Close() //nolint:errcheck,gosec
		}
		return nil, err
	}
	conn.Client = client
	return conn, nil
}

// dialHost connects to h. via, when set, is the client to tunnel through;
// otherwise h's own ProxyJump (if any) is followed.
func (d *Dialer) dialHost(ctx context.Context, label string, h HostConfig, via *ssh.Client, conn *Conn, depth int) (*ssh.Client, error) {
	if depth > maxJumpDepth {
		return nil, fmt.Errorf("%s: ProxyJump chain deeper than %d hops (loop?)", label, maxJumpDepth)
	}
	if via == nil && h.ProxyJump != "" {
		hops, err := ParseProxyJump(h.ProxyJump)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		for i, hop := range hops {
			hc, err := d.Resolver.Resolve(ctx, hop.Host)
			if err != nil {
				return nil, fmt.Errorf("%s: jump host %s: %w", label, hop.Host, err)
			}
			if hop.User != "" {
				hc.User = hop.User
			}
			if hop.Port != 0 {
				hc.Port = hop.Port
			}
			if i > 0 {
				// Later hops are reached through the previous one; their own
				// ProxyJump does not apply (as with ssh -J a,b).
				hc.ProxyJump = ""
			}
			c, err := d.dialHost(ctx, hop.Host, hc, via, conn, depth+1)
			if err != nil {
				return nil, err
			}
			conn.hops = append(conn.hops, c)
			via = c
		}
	}

	addr := h.Address()
	conn.Route = append(conn.Route, label)
	log := d.log().With("host", label, "addr", addr)

	dctx, cancel := context.WithTimeout(ctx, h.ConnectTimeout)
	defer cancel()
	var raw net.Conn
	var err error
	if via != nil {
		raw, err = via.DialContext(dctx, "tcp", addr)
	} else {
		raw, err = d.netDial(dctx, "tcp", addr)
	}
	if err != nil {
		return nil, &ConnectError{Alias: label, Address: addr, Timeout: h.ConnectTimeout, Err: err}
	}

	// Bound the handshake by ConnectTimeout, except while the operator is
	// answering a prompt (host key, passphrase).
	guard := newHandshakeGuard(ctx, raw, h.ConnectTimeout)
	defer guard.stop()
	prompter := guardedPrompter{Prompter: d.Prompter, guard: guard}

	auth, err := buildAuth(ctx, label, h, d.AgentSock, prompter, d.Passphrases)
	if err != nil {
		raw.Close() //nolint:errcheck,gosec
		return nil, err
	}
	if auth.closer != nil {
		defer auth.closer.Close() //nolint:errcheck
	}
	verifier, err := newHostKeyVerifier(ctx, label, h, d.Trust, prompter)
	if err != nil {
		raw.Close() //nolint:errcheck,gosec
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:              h.User,
		Auth:              []ssh.AuthMethod{auth.method},
		HostKeyCallback:   verifier.callback,
		HostKeyAlgorithms: verifier.algorithms(addr),
		Timeout:           h.ConnectTimeout,
	}

	c, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	guard.stop()
	if err != nil {
		raw.Close() //nolint:errcheck,gosec
		return nil, classifyHandshakeError(label, h, auth, err)
	}
	log.Debug("ssh connected", "user", h.User)
	return ssh.NewClient(c, chans, reqs), nil
}

func (d *Dialer) netDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.NetDial != nil {
		return d.NetDial(ctx, network, addr)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (d *Dialer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// handshakeGuard expires the connection's deadline after timeout, or when
// ctx ends. pause/resume suspend the timer while the operator answers a
// prompt.
type handshakeGuard struct {
	raw     net.Conn
	timeout time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
	done    chan struct{}
}

func newHandshakeGuard(ctx context.Context, raw net.Conn, timeout time.Duration) *handshakeGuard {
	g := &handshakeGuard{raw: raw, timeout: timeout, done: make(chan struct{})}
	g.timer = time.AfterFunc(timeout, g.expire)
	go func() {
		select {
		case <-ctx.Done():
			g.expire()
		case <-g.done:
		}
	}()
	return g
}

func (g *handshakeGuard) expire() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.stopped {
		g.raw.SetDeadline(time.Now()) //nolint:errcheck,gosec
	}
}

func (g *handshakeGuard) pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer.Stop()
}

func (g *handshakeGuard) resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.stopped {
		g.timer.Reset(g.timeout)
	}
}

func (g *handshakeGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return
	}
	g.stopped = true
	g.timer.Stop()
	close(g.done)
}

// guardedPrompter pauses the handshake timer around each prompt.
type guardedPrompter struct {
	Prompter
	guard *handshakeGuard
}

func (p guardedPrompter) ConfirmHostKey(ctx context.Context, q HostKeyQuestion) (HostKeyDecision, error) {
	p.guard.pause()
	defer p.guard.resume()
	return p.Prompter.ConfirmHostKey(ctx, q)
}

func (p guardedPrompter) Passphrase(ctx context.Context, alias, keyPath string, retry bool) (*secret.Buffer, error) {
	p.guard.pause()
	defer p.guard.resume()
	return p.Prompter.Passphrase(ctx, alias, keyPath, retry)
}

// ConnectError is a TCP-level failure to reach a host.
type ConnectError struct {
	Alias, Address string
	Timeout        time.Duration
	Err            error
}

func (e *ConnectError) Error() string {
	return fmt.Sprintf("cannot reach %s at %s (timeout %s): %v; check the host is up, then press r to retry", e.Alias, e.Address, e.Timeout, e.Err)
}

func (e *ConnectError) Unwrap() error { return e.Err }

func classifyHandshakeError(alias string, h HostConfig, auth *authSetup, err error) error {
	var changed *HostKeyChangedError
	var rejected *HostKeyRejectedError
	switch {
	case errors.As(err, &changed), errors.As(err, &rejected):
		return err
	case strings.Contains(err.Error(), "unable to authenticate"):
		return &AuthError{Alias: alias, User: h.User, Tried: auth.tried.list(), AgentPresent: auth.agentPresent, Err: err}
	case errors.Is(err, io.EOF), errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return &ConnectError{Alias: alias, Address: h.Address(), Timeout: h.ConnectTimeout, Err: err}
	}
	return fmt.Errorf("%s: ssh handshake: %w", alias, err)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
