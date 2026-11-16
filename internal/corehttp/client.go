// Package corehttp is a typed client for stellar-core's admin HTTP endpoint.
//
// Only six commands exist here: info, quorum, peers, upgrades, sorobaninfo
// and dumpproposedsettings (CLAUDE.md hard rule 3). The command type is
// unexported and built only by this package, so code elsewhere cannot send
// anything else (tx, bans, surveys, manualclose, load generation).
package corehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Timeouts from SPEC.md "Reaching the admin HTTP endpoint".
const (
	InfoTimeout    = 5 * time.Second
	DefaultTimeout = 15 * time.Second
)

// maxBody bounds a response; quorum?transitive=true on pubnet is the largest
// and stays well under this.
const maxBody = 16 << 20

// Command is one admin request. Build it with the functions below.
type Command struct {
	name  string
	query url.Values
}

// allowed lists every command name this package can build.
var allowed = map[string]bool{
	"info": true, "quorum": true, "peers": true, "upgrades": true,
	"sorobaninfo": true, "dumpproposedsettings": true,
}

// Name returns the command name.
func (c Command) Name() string { return c.name }

// Path returns the request path and query, e.g. "/quorum?compact=false".
func (c Command) Path() string {
	p := "/" + c.name
	if len(c.query) > 0 {
		p += "?" + c.query.Encode()
	}
	return p
}

// String is the form `stellar-core http-command` takes, e.g.
// "quorum?compact=false".
func (c Command) String() string { return strings.TrimPrefix(c.Path(), "/") }

func (c Command) timeout() time.Duration {
	if c.name == "info" {
		return InfoTimeout
	}
	return DefaultTimeout
}

// CmdInfo is the info command.
func CmdInfo() Command { return Command{name: "info"} }

// CmdQuorum is quorum?compact=false, or with transitive=true the network-wide
// intersection analysis (slow on pubnet; on demand only).
func CmdQuorum(transitive bool) Command {
	q := url.Values{"compact": {"false"}}
	if transitive {
		q.Set("transitive", "true")
	}
	return Command{name: "quorum", query: q}
}

// CmdPeers is the peers command.
func CmdPeers() Command { return Command{name: "peers"} }

// CmdUpgradesGet is upgrades?mode=get.
func CmdUpgradesGet() Command {
	return Command{name: "upgrades", query: url.Values{"mode": {"get"}}}
}

// CmdSorobanInfoDetailed is sorobaninfo?format=detailed.
func CmdSorobanInfoDetailed() Command {
	return Command{name: "sorobaninfo", query: url.Values{"format": {"detailed"}}}
}

// CmdDumpProposedSettings is dumpproposedsettings?blob=<ConfigUpgradeSetKey>.
func CmdDumpProposedSettings(blob string) Command {
	return Command{name: "dumpproposedsettings", query: url.Values{"blob": {blob}}}
}

// Transport performs one admin request and returns the HTTP status and body.
// The SSH tunnel and the run-on-host fallback both implement it.
type Transport interface {
	Do(ctx context.Context, cmd Command) (status int, body []byte, err error)
	// Name says how requests travel, for the host detail view.
	Name() string
}

// Error is a failed admin request (SPEC.md "Errors": command, HTTP status,
// first 500 bytes of the body).
type Error struct {
	Command string
	Status  int
	Body    string
	Err     error
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("admin %s: %v; check the service is running", e.Command, e.Err)
	default:
		return fmt.Sprintf("admin %s: HTTP %d: %s", e.Command, e.Status, e.Body)
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Client sends admin commands over a Transport.
type Client struct {
	t Transport
}

// New returns a client.
func New(t Transport) *Client { return &Client{t: t} }

// Transport returns the client's transport.
func (c *Client) Transport() Transport { return c.t }

// Raw sends cmd and returns the body after checking the status.
func (c *Client) Raw(ctx context.Context, cmd Command) ([]byte, error) {
	if !allowed[cmd.name] {
		return nil, fmt.Errorf("admin command %q is not allowed", cmd.name)
	}
	ctx, cancel := context.WithTimeout(ctx, cmd.timeout())
	defer cancel()
	status, body, err := c.t.Do(ctx, cmd)
	if err != nil {
		return nil, &Error{Command: cmd.String(), Err: err}
	}
	if status != http.StatusOK {
		return nil, &Error{Command: cmd.String(), Status: status, Body: snippet(body)}
	}
	return body, nil
}

func snippet(b []byte) string {
	if len(b) > 500 {
		b = b[:500]
	}
	return strings.TrimSpace(string(b))
}

func getJSON[T any](ctx context.Context, c *Client, cmd Command) (*T, []byte, error) {
	body, err := c.Raw(ctx, cmd)
	if err != nil {
		return nil, nil, err
	}
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, body, &Error{Command: cmd.String(), Status: http.StatusOK, Body: snippet(body), Err: fmt.Errorf("decode: %w", err)}
	}
	return &v, body, nil
}

// Info fetches info. The raw JSON is returned too, for the Info tab and copy.
func (c *Client) Info(ctx context.Context) (*InfoResponse, []byte, error) {
	r, raw, err := getJSON[InfoResponse](ctx, c, CmdInfo())
	if err != nil {
		return nil, raw, err
	}
	return r, raw, nil
}

// Quorum fetches quorum?compact=false (and transitive analysis if asked).
func (c *Client) Quorum(ctx context.Context, transitive bool) (*QuorumResponse, []byte, error) {
	return getJSON[QuorumResponse](ctx, c, CmdQuorum(transitive))
}

// Peers fetches peers.
func (c *Client) Peers(ctx context.Context) (*PeersResponse, []byte, error) {
	return getJSON[PeersResponse](ctx, c, CmdPeers())
}

// Upgrades fetches upgrades?mode=get.
func (c *Client) Upgrades(ctx context.Context) (*UpgradesResponse, []byte, error) {
	return getJSON[UpgradesResponse](ctx, c, CmdUpgradesGet())
}

// HTTPTransport is the SSH tunnel transport: an http.Client whose dialer
// opens a direct-tcpip channel to 127.0.0.1:<port> on the host.
type HTTPTransport struct {
	client *http.Client
	port   int
}

// Dialer opens a connection to addr on the host (sshx.Conn.DialContext).
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// NewHTTPTransport returns a tunnel transport for port on the host.
func NewHTTPTransport(dial Dialer, port int) *HTTPTransport {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dial(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		},
		DisableKeepAlives:     true, // one channel per request; nothing idles
		ResponseHeaderTimeout: DefaultTimeout,
		Proxy:                 nil,
	}
	return &HTTPTransport{client: &http.Client{Transport: tr}, port: port}
}

// Name implements Transport.
func (t *HTTPTransport) Name() string { return "ssh tunnel" }

// Do implements Transport.
func (t *HTTPTransport) Do(ctx context.Context, cmd Command) (int, []byte, error) {
	u := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", t.port), Path: "/" + cmd.name, RawQuery: cmd.query.Encode()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// ErrForwardingRefused is how a tunnel transport reports that sshd refused
// the direct-tcpip channel; the caller then switches to the fallback.
var ErrForwardingRefused = errors.New("ssh server refused port forwarding")
