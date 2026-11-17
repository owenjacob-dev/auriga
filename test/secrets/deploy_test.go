package secrets

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// importSeed is the operator's pasted seed in this test; the stellar-core
// shim derives importAddress from it.
const (
	importSeed    = "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI"
	importAddress = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
)

// TestDeployImportDoesNotLeak deploys a config with an imported seed
// through the live backend, the SSH server and the host helpers, with
// --debug logging and the audit log on. The seed may exist only in the
// staged and installed config on the host, never on this machine.
func TestDeployImportDoesNotLeak(t *testing.T) { deployDoesNotLeak(t, stellarcfg.SeedImport) }

// TestDeployGenerateDoesNotLeak is the same with a seed generated on the
// host (the stellar-core shim's gen-seed prints importSeed), which this
// machine must only ever see as its public key.
func TestDeployGenerateDoesNotLeak(t *testing.T) { deployDoesNotLeak(t, stellarcfg.SeedGenerate) }

func deployDoesNotLeak(t *testing.T, mode stellarcfg.SeedMode) {
	dirs := env(t)
	home := os.Getenv("HOME")
	hostDir := t.TempDir() // the validator host; not scanned
	cfgPath := filepath.Join(hostDir, "stellar-core.cfg")
	writeTestFile(t, cfgPath, []byte("NETWORK_PASSPHRASE=\"Test SDF Network ; September 2015\"\nNODE_SEED=\""+hostSeed+" canary-a\"\n"))

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub, _ := ssh.NewPublicKey(priv.Public())
	srv := sshtest.NewServer(t)
	srv.AuthorizedKeys = []ssh.PublicKey{pub}
	srv.Exec = fakeHostExec(t, hostDir)
	// The node's admin endpoint, reached through the SSH tunnel.
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			io.WriteString(w, `{"info":{"state":"Synced!","ledger":{"num":42,"age":1}}}`)
		case "/quorum":
			io.WriteString(w, `{"node":"canary-a","qset":{"fail_at":2,"missing":[]},"transitive":{"intersection":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer admin.Close()
	srv.AllowForwarding = true
	srv.DialTarget = func(string) (net.Conn, error) { return net.Dial("tcp", admin.Listener.Addr().String()) }
	srv.Start()
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, keyPath, pem.EncodeToMemory(pemBlock))
	writeTestFile(t, filepath.Join(home, ".ssh", "config"), []byte(fmt.Sprintf(
		"Host canary-a\n  HostName 127.0.0.1\n  Port %d\n  User op\n  IdentityFile %s\n  IdentitiesOnly yes\n",
		srv.Port(), keyPath)))

	if err := os.MkdirAll(dirs.State, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(dirs.State, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	b := app.NewLiveBackend(sshx.Env{Home: home, User: "op", ConfigPath: filepath.Join(home, ".ssh", "config")}, "", dirs,
		slog.New(secret.NewLogHandler(logFile, slog.LevelDebug)))
	defer b.Close()
	connected := make(chan struct{})
	go func() {
		for e := range b.Events() {
			switch e := e.(type) {
			case sshx.StateEvent:
				if e.State == sshx.StateConnected {
					select {
					case <-connected:
					default:
						close(connected)
					}
				}
			case sshx.PromptEvent:
				if e.HostKey != nil {
					e.AnswerHostKey(sshx.HostKeyTrustSession)
				} else if e.Sudo {
					e.AnswerPassphrase(secret.FromBytes([]byte(sudoCanary)))
				} else {
					e.Cancel()
				}
			}
		}
	}()
	b.Connect("canary-a")
	select {
	case <-connected:
	case <-time.After(20 * time.Second):
		t.Fatal("not connected")
	}

	seed := &stellarcfg.Seed{Mode: mode, PublicKey: importAddress, Alias: "canary-a"}
	if mode == stellarcfg.SeedGenerate {
		seed.PublicKey = ""
	}
	render := func(s *stellarcfg.Seed) []byte {
		return []byte("# rendered by auriga\nNETWORK_PASSPHRASE = \"Test SDF Network ; September 2015\"\nNODE_SEED = \"" + s.SeedMarker() + "\"\n")
	}
	rendered := render(seed)
	target := deploy.Target{Alias: "canary-a", ConfigPath: cfgPath, Service: "stellar-core", Rendered: rendered,
		Seed: seed, Running: true, HTTPPort: 11626}
	var paste *secret.Buffer
	if mode == stellarcfg.SeedImport {
		paste = secret.FromBytes([]byte(importSeed))
		target.Import = paste
	}
	var events bytes.Buffer
	opt := deploy.Options{Org: "canary", Network: "testnet", SyncTimeout: 50 * time.Millisecond, PollEvery: 10 * time.Millisecond,
		MinWorkDirKB: 1, MinConfigFSKB: 1}
	// A generated key comes back as its public key only; the re-plan puts
	// it in the render's seed, as the wizard does.
	opt.Replan = func(generated map[string]string) ([]deploy.Target, error) {
		re := target
		s := *seed
		s.PublicKey = generated[target.Alias]
		re.Seed, re.Rendered = &s, render(&s)
		return []deploy.Target{re}, nil
	}
	c := deploy.New(b.DeployOps(), b.AuditLog(), []deploy.Target{target}, opt, func(e deploy.Event) {
		fmt.Fprintf(&events, "%s %s %d %s\n", e.Host, e.Step, e.Status, e.Detail)
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.Run(ctx); err != nil {
		t.Fatalf("%v\n%s", err, events.String())
	}
	if o := c.Outcomes()["canary-a"]; o != deploy.Deployed {
		t.Fatalf("outcome %s\n%s", o, events.String())
	}

	// The seed reached the host (over stdin, or generated there) and is in
	// its config there.
	installed, _ := os.ReadFile(cfgPath)
	if !bytes.Contains(installed, []byte(`NODE_SEED = "`+importSeed+` canary-a"`)) {
		t.Fatalf("seed not installed on the host:\n%s\n%s", installed, events.String())
	}
	if argv, _ := os.ReadFile(filepath.Join(hostDir, "argv.log")); bytes.Contains(argv, []byte(importSeed)) {
		t.Error("seed appeared in an argv on the host")
	}
	if paste != nil && paste.Len() != 0 {
		t.Error("pasted seed not zeroed after staging")
	}
	entries, _, _ := b.AuditLog().Read()
	if len(entries) < 4 {
		t.Errorf("audit log has %d entries; the scan would prove little", len(entries))
	}

	planted = append(planted, importSeed, hostSeed, sudoCanary)
	t.Cleanup(func() { planted = planted[:2] })
	scan(t, []string{dirs.Config, dirs.State, home}, events.Bytes())
}
