package corehttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

func fixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "core", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// canned is a Transport that answers from a map of command paths.
type canned struct {
	bodies map[string][]byte
	status int
	err    error
	seen   []string
}

func (c *canned) Name() string { return "canned" }
func (c *canned) Do(_ context.Context, cmd Command) (int, []byte, error) {
	c.seen = append(c.seen, cmd.Path())
	if c.err != nil {
		return 0, nil, c.err
	}
	b, ok := c.bodies[cmd.Path()]
	if !ok {
		return 404, []byte("not found"), nil
	}
	if c.status != 0 {
		return c.status, b, nil
	}
	return 200, b, nil
}

func TestCommandsAreTheSixAllowed(t *testing.T) {
	cmds := []Command{CmdInfo(), CmdQuorum(false), CmdQuorum(true), CmdPeers(), CmdUpgradesGet(),
		CmdSorobanInfoDetailed(), CmdDumpProposedSettings("AAAA/b+c=")}
	want := []string{"/info", "/quorum?compact=false", "/quorum?compact=false&transitive=true", "/peers",
		"/upgrades?mode=get", "/sorobaninfo?format=detailed", "/dumpproposedsettings?blob=AAAA%2Fb%2Bc%3D"}
	names := map[string]bool{}
	for i, c := range cmds {
		if c.Path() != want[i] {
			t.Errorf("%s: path %q, want %q", c.Name(), c.Path(), want[i])
		}
		names[c.Name()] = true
	}
	if !reflect.DeepEqual(names, allowed) {
		t.Errorf("constructors build %v, allowlist is %v", names, allowed)
	}
	// A hand-made Command for anything else is refused before any I/O.
	tr := &canned{}
	for _, bad := range []string{"tx", "manualclose", "ban", "surveytopology", "generateload", "upgrades-set"} {
		if _, err := New(tr).Raw(context.Background(), Command{name: bad}); err == nil {
			t.Errorf("%s allowed", bad)
		}
	}
	if len(tr.seen) != 0 {
		t.Errorf("transport called for refused commands: %v", tr.seen)
	}
}

func TestDecodeSyntheticInfo(t *testing.T) {
	tr := &canned{bodies: map[string][]byte{"/info": fixture(t, "synthetic/info-pubnet-synced.json")}}
	r, raw, err := New(tr).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := r.Info
	if i.State != "Synced!" || i.Ledger.Num != 61234567 || i.Ledger.Version != 29 || i.ProtocolVersion != 30 ||
		i.Peers.Authenticated != 12 || i.Quorum.QSet == nil || *i.Quorum.QSet.FailAt != 3 || i.Quorum.QSet.Agree.N() != 18 ||
		!*i.Quorum.Transitive.Intersection || i.Armed() == "" || len(raw) == 0 {
		t.Errorf("decoded %+v", i)
	}
}

func TestDecodeLegacyQuorumLayout(t *testing.T) {
	tr := &canned{bodies: map[string][]byte{"/info": fixture(t, "synthetic/info-legacy-quorum-by-ledger.json")}}
	r, _, err := New(tr).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q := r.Info.Quorum
	if q.QSet == nil || q.QSet.Agree.N() != 21 || q.QSet.Missing.N() != 0 || q.Transitive == nil || !*q.Transitive.Intersection {
		t.Errorf("legacy quorum %+v / %+v", q.QSet, q.Transitive)
	}
}

func TestCountOrList(t *testing.T) {
	var v struct {
		A CountOrList `json:"a"`
		B CountOrList `json:"b"`
		C CountOrList `json:"c"`
	}
	if err := jsonUnmarshal(`{"a": 3, "b": ["G1","G2"], "c": null}`, &v); err != nil {
		t.Fatal(err)
	}
	if v.A.N() != 3 || v.B.N() != 2 || v.C.N() != 0 {
		t.Errorf("%+v", v)
	}
}

func TestQSetValueNesting(t *testing.T) {
	var q QSetValue
	if err := jsonUnmarshal(`{"t": 2, "v": ["GA", {"t": 1, "v": ["GB", "GC"]}]}`, &q); err != nil {
		t.Fatal(err)
	}
	if q.Threshold != 2 || !reflect.DeepEqual(q.Nodes, []string{"GA"}) || len(q.Inner) != 1 || q.Inner[0].Threshold != 1 || len(q.Inner[0].Nodes) != 2 {
		t.Errorf("%+v", q)
	}
}

func TestErrors(t *testing.T) {
	_, err := New(&canned{bodies: map[string][]byte{"/info": []byte(strings.Repeat("x", 900))}, status: 500}).Raw(context.Background(), CmdInfo())
	var e *Error
	if !errors.As(err, &e) || e.Status != 500 || len(e.Body) != 500 || e.Command != "info" {
		t.Errorf("err = %v", err)
	}
	_, _, err = New(&canned{bodies: map[string][]byte{"/peers": []byte("not json")}}).Peers(context.Background())
	if !errors.As(err, &e) || !strings.Contains(err.Error(), "decode") {
		t.Errorf("bad JSON: %v", err)
	}
	_, err = New(&canned{err: io.ErrUnexpectedEOF}).Raw(context.Background(), CmdPeers())
	if !errors.As(err, &e) || !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "check the service is running") {
		t.Errorf("transport error: %v", err)
	}
}

// tunnel sets up an admin endpoint reachable only through an SSH server.
func tunnel(t *testing.T, allowForwarding bool) (*ssh.Client, int, *[]string) {
	t.Helper()
	var paths []string
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path == "/info" {
			w.Write(fixture(t, "synthetic/info-pubnet-synced.json"))
			return
		}
		http.Error(w, "unknown", http.StatusNotFound)
	}))
	t.Cleanup(admin.Close)
	srv := sshtest.NewServer(t)
	client := sshtest.NewSigner(t)
	srv.AuthorizedKeys = []ssh.PublicKey{client.PublicKey()}
	srv.AllowForwarding = allowForwarding
	adminAddr := admin.Listener.Addr().String()
	srv.DialTarget = func(addr string) (net.Conn, error) {
		if !strings.HasPrefix(addr, "127.0.0.1:") {
			return nil, errors.New("unexpected target " + addr)
		}
		return net.Dial("tcp", adminAddr)
	}
	srv.Start()
	c, err := ssh.Dial("tcp", srv.Addr, &ssh.ClientConfig{
		User: "op", Auth: []ssh.AuthMethod{ssh.PublicKeys(client)},
		HostKeyCallback: ssh.FixedHostKey(srv.HostKey.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, 11626, &paths
}

func TestHTTPTransportThroughSSH(t *testing.T) {
	c, port, paths := tunnel(t, true)
	cl := New(NewHTTPTransport(c.DialContext, port))
	r, _, err := cl.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Info.State != "Synced!" || len(*paths) != 1 || (*paths)[0] != "/info" {
		t.Errorf("state %q, paths %v", r.Info.State, *paths)
	}
	if _, err := cl.Raw(context.Background(), CmdQuorum(false)); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}
	if (*paths)[1] != "/quorum?compact=false" {
		t.Errorf("query not encoded as expected: %v", *paths)
	}
}

type fallback struct{ calls int }

func (f *fallback) Name() string { return "on host" }
func (f *fallback) Do(context.Context, Command) (int, []byte, error) {
	f.calls++
	return 200, []byte(`{"info":{"state":"Synced!"}}`), nil
}

func TestAdaptiveFallsBackWhenForwardingRefused(t *testing.T) {
	c, port, paths := tunnel(t, false)
	fb := &fallback{}
	a := NewAdaptive(NewHTTPTransport(c.DialContext, port), fb, sshx.IsForwardingRefused)
	cl := New(a)
	if a.Name() != "ssh tunnel" {
		t.Errorf("starts as %q", a.Name())
	}
	for range 2 {
		r, _, err := cl.Info(context.Background())
		if err != nil || r.Info.State != "Synced!" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if fb.calls != 2 || a.Name() != "on host" || len(*paths) != 0 {
		t.Errorf("fallback calls %d, name %q, tunnel requests %v", fb.calls, a.Name(), *paths)
	}
	a.Reset()
	if a.Name() != "ssh tunnel" {
		t.Error("Reset did not return to the tunnel")
	}
}

func TestAdaptiveKeepsOtherErrors(t *testing.T) {
	fb := &fallback{}
	a := NewAdaptive(&canned{err: io.EOF}, fb, sshx.IsForwardingRefused)
	if _, _, err := a.Do(context.Background(), CmdInfo()); !errors.Is(err, io.EOF) || fb.calls != 0 {
		t.Errorf("err %v, fallback calls %d", err, fb.calls)
	}
}
