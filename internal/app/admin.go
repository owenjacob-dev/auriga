package app

import (
	"context"
	"sync"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
)

// DefaultHTTPPort is stellar-core's default admin port.
const DefaultHTTPPort = 11626

// tunnelTransport sends each request over a fresh direct-tcpip channel,
// holding one of the host's channel slots for the request's duration.
type tunnelTransport struct {
	pool  *sshx.Pool
	alias string
	port  int
}

func (t *tunnelTransport) Name() string { return "ssh tunnel" }

func (t *tunnelTransport) Do(ctx context.Context, cmd corehttp.Command) (status int, body []byte, err error) {
	runErr := t.pool.Run(ctx, t.alias, func(ctx context.Context, c *sshx.Conn) error {
		status, body, err = corehttp.NewHTTPTransport(c.DialContext, t.port).Do(ctx, cmd)
		return nil
	})
	if runErr != nil {
		return 0, nil, runErr
	}
	return status, body, err
}

// hostTransport runs the request on the host with the admin-get helper.
type hostTransport struct {
	runner     *remote.Runner
	alias      string
	port       int
	configPath string

	mu  sync.Mutex
	via string
}

func (t *hostTransport) Name() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.via == "" {
		return "on host"
	}
	return "on host (" + t.via + ")"
}

func (t *hostTransport) Do(ctx context.Context, cmd corehttp.Command) (int, []byte, error) {
	res, err := t.runner.AdminGet(ctx, t.alias, t.port, cmd.String(), t.configPath)
	if err != nil {
		return 0, nil, err
	}
	t.mu.Lock()
	t.via = res.Via
	t.mu.Unlock()
	return res.Status, res.Body, nil
}

type adminKey struct {
	alias, configPath string
	port              int
}

// AdminClient returns the admin client for a host: the SSH tunnel first, and
// the on-host fallback once sshd refuses forwarding. Clients are cached per
// host, port and config path so the fallback choice sticks.
func (b *LiveBackend) AdminClient(alias string, port int, configPath string) *corehttp.Client {
	if port == 0 {
		port = DefaultHTTPPort
	}
	k := adminKey{alias, configPath, port}
	b.adminMu.Lock()
	defer b.adminMu.Unlock()
	if c, ok := b.admin[k]; ok {
		return c
	}
	tr := corehttp.NewAdaptive(
		&tunnelTransport{pool: b.pool, alias: alias, port: port},
		&hostTransport{runner: b.runner, alias: alias, port: port, configPath: configPath},
		sshx.IsForwardingRefused,
	)
	c := corehttp.New(tr)
	b.admin[k] = c
	return c
}

// forgetAdmin drops cached clients for alias (removed or reset by refresh).
func (b *LiveBackend) forgetAdmin(alias string) {
	b.adminMu.Lock()
	defer b.adminMu.Unlock()
	for k := range b.admin {
		if k.alias == alias {
			delete(b.admin, k)
		}
	}
}
