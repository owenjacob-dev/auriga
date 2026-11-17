package secrets

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

// The seed the stellar-core shim knows (internal/strkey test vector).
const hostSeed = "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4"

// Canaries for secrets that are not seed-shaped, so pattern redaction cannot
// hide a leak: these must stay out structurally.
const (
	passphraseCanary = "PASSPHRASE-CANARY-7f3a"
	sudoCanary       = "SUDO-CANARY-91be"
)

// fakeHostExec runs commands the way sshd would on a host with the shims
// from internal/remote/testdata (sudo needs sudoCanary).
func fakeHostExec(t *testing.T, hostDir string) sshtest.ExecFunc {
	t.Helper()
	bin := filepath.Join(hostDir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shims, _ := filepath.Glob("../../internal/remote/testdata/shims/*")
	for _, s := range shims {
		abs, _ := filepath.Abs(s)
		if err := os.Symlink(abs, filepath.Join(bin, filepath.Base(s))); err != nil {
			t.Fatal(err)
		}
	}
	dash, err := exec.LookPath("dash")
	if err != nil {
		dash = "/bin/sh"
	}
	if err := os.Symlink(dash, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + hostDir, "SHIM_WORKDIR=" + hostDir,
		"SHIM_SUDO=password", "SHIM_SUDO_PASSWORD=" + sudoCanary,
		"SHIM_LOG=" + filepath.Join(hostDir, "argv.log"),
	}
	return func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		c := exec.Command("bash", "-c", cmd)
		c.Env, c.Dir, c.Stdout, c.Stderr = env, hostDir, stdout, stderr
		in, _ := c.StdinPipe()
		if err := c.Start(); err != nil {
			return 127
		}
		go func() { io.Copy(in, stdin); in.Close() }()
		err := c.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		return 0
	}
}

func TestSSHFlowDoesNotLeak(t *testing.T) {
	dirs := env(t)
	home := os.Getenv("HOME")
	hostDir := t.TempDir() // the "validator host"; not scanned

	// Host side: a config whose seed must never reach this machine.
	cfgPath := filepath.Join(hostDir, "stellar-core.cfg")
	cfg := "NETWORK_PASSPHRASE=\"Test SDF Network ; September 2015\"\nNODE_SEED=\"" + hostSeed + " canary-a\"\nNODE_HOME_DOMAIN=\"canary.example\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o640); err != nil {
		t.Fatal(err)
	}

	// Client key encrypted with the passphrase canary.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphraseCanary))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	writeTestFile(t, keyPath, pem.EncodeToMemory(block))
	pub, _ := ssh.NewPublicKey(priv.Public())

	srv := sshtest.NewServer(t)
	srv.AuthorizedKeys = []ssh.PublicKey{pub}
	srv.Exec = fakeHostExec(t, hostDir)
	srv.Start()

	writeTestFile(t, filepath.Join(home, ".ssh", "config"), []byte(fmt.Sprintf(
		"Host canary-a\n  HostName 127.0.0.1\n  Port %d\n  User op\n  IdentityFile %s\n  IdentitiesOnly yes\n",
		srv.Port(), keyPath)))
	inv := inventory.New()
	inv.AddHost("canary-a")
	inv.Host("canary-a").ConfigPath = cfgPath
	if err := inv.Save(dirs.Config); err != nil {
		t.Fatal(err)
	}

	// --debug logging into the state directory.
	if err := os.MkdirAll(dirs.State, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(dirs.State, "debug.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	log := slog.New(secret.NewLogHandler(logFile, slog.LevelDebug))

	sshEnv := sshx.Env{Home: home, User: "op", ConfigPath: filepath.Join(home, ".ssh", "config")}
	backend := app.NewLiveBackend(sshEnv, "", dirs, log)
	m := app.New(app.Options{Version: "test", Backend: backend, NoColor: true, DisableClock: true})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	waitFor := func(text string) {
		t.Helper()
		teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(text)) },
			teatest.WithDuration(20*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	}
	typeSecret := func(s string) {
		for _, r := range s {
			tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	}

	waitFor("Unknown host key")
	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	waitFor("Passphrase for")
	typeSecret(passphraseCanary)
	waitFor("sudo password for canary-a")
	typeSecret(sudoCanary)
	// The probe is done once the key derived on the host is in the inventory.
	// (Waiting on screen text is fragile: the renderer sends only changed
	// cells, so a string can arrive split.)
	deadline := time.Now().Add(20 * time.Second)
	for {
		if inv, err := inventory.Load(dirs.Config); err == nil && inv.Host("canary-a").PublicKey != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe never saved the public key")
		}
		time.Sleep(50 * time.Millisecond)
	}
	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(10*time.Second))
	out, _ := io.ReadAll(tm.FinalOutput(t))
	tm.FinalModel(t).(app.Model).Close()

	// The host saw the seed only on its own side: never in any argv.
	if b, _ := os.ReadFile(filepath.Join(hostDir, "argv.log")); bytes.Contains(b, []byte(hostSeed)) {
		t.Error("seed appeared in a stellar-core argv on the host")
	}
	saved, err := inventory.Load(dirs.Config)
	if err != nil || saved.Host("canary-a").PublicKey != "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H" {
		t.Errorf("inventory after probe: %+v %v", saved, err)
	}
	if fi, err := os.Stat(filepath.Join(dirs.State, "debug.log")); err != nil || fi.Size() == 0 {
		t.Error("debug log is empty; the scan would prove nothing")
	}

	planted = append(planted, hostSeed, passphraseCanary, sudoCanary)
	t.Cleanup(func() { planted = planted[:2] })
	scan(t, []string{dirs.Config, dirs.State, home}, out)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
