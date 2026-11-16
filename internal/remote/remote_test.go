package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

// Known-good vectors (internal/strkey tests); the stellar-core shim maps
// these seeds to these addresses.
const (
	testSeed    = "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4"
	testAddress = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
)

// fakeHost runs commands locally the way sshd would: the login shell (bash)
// runs the command line, with shims for sudo, stellar-core, systemctl and
// timedatectl first on PATH, and "sh" pointing at dash or bash.
type fakeHost struct {
	bin string
	dir string
	env []string
	log string
}

func newFakeHost(t *testing.T, shell string, extraEnv ...string) *fakeHost {
	t.Helper()
	shellPath, err := exec.LookPath(shell)
	if err != nil {
		t.Skipf("%s not installed", shell)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	shims, _ := filepath.Glob("testdata/shims/*")
	for _, s := range shims {
		abs, _ := filepath.Abs(s)
		if err := os.Symlink(abs, filepath.Join(bin, filepath.Base(s))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(shellPath, filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	h := &fakeHost{bin: bin, dir: dir, log: filepath.Join(dir, "stellar-core.log")}
	h.env = append([]string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + dir,
		"SHIM_LOG=" + h.log,
		"SHIM_WORKDIR=" + dir,
	}, extraEnv...)
	return h
}

func (h *fakeHost) Exec(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Env, c.Dir = h.env, h.dir
	c.Stdout, c.Stderr = stdout, stderr
	// Like sshx.Conn.Exec: do not wait for stdin to reach EOF.
	in, err := c.StdinPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	if stdin != nil {
		go func() { io.Copy(in, stdin); in.Close() }()
	} else {
		in.Close()
	}
	err = c.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &sshx.ExitError{Status: ee.ExitCode()}
	}
	return err
}

func (h *fakeHost) argvLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// countingPrompter returns scripted sudo passwords.
type countingPrompter struct {
	mu        sync.Mutex
	passwords []string
	asked     []bool // retry flags
}

func (p *countingPrompter) SudoPassword(_ context.Context, _ string, retry bool) (*secret.Buffer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, retry)
	if len(p.passwords) == 0 {
		return nil, errors.New("no password")
	}
	pw := p.passwords[0]
	p.passwords = p.passwords[1:]
	return secret.FromBytes([]byte(pw)), nil
}

var shells = []string{"dash", "bash"}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// runInline runs a test-only script body through the same framing as Run.
func (r *Runner) runInline(ctx context.Context, alias, body string, args []string, opt RunOptions) (*Result, error) {
	st, err := r.ensureSudo(ctx, alias)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := r.execFramed(ctx, alias, st, []byte(body), args, opt)
	return &Result{Stdout: stdout, Stderr: stderr}, err
}

func TestStdinProtocol(t *testing.T) {
	nasty := `p'a"s$(touch PWNED)` + "`touch PWNED2`;\\"
	for _, shell := range shells {
		for _, mode := range []string{"nopasswd", "password"} {
			t.Run(shell+"/"+mode, func(t *testing.T) {
				h := newFakeHost(t, shell, "SHIM_SUDO="+mode, "SHIM_SUDO_PASSWORD=hunter2 'x")
				p := &countingPrompter{passwords: []string{"hunter2 'x"}}
				r := newDirectRunner(map[string]Conn{"a": h}, p)

				// The helper reads the secret after the script.
				res, err := r.runInline(ctx(t), "a", `auriga_read_secret s; printf '%s|%s|%s' "${#s}" "$1" "$(id -u)"`,
					[]string{"arg with 'quote'"}, RunOptions{Secret: secret.FromBytes([]byte(nasty))})
				if err != nil {
					t.Fatalf("%v: %s", err, res.Stderr)
				}
				want := fmt.Sprintf("%d|arg with 'quote'|", len(nasty))
				if !strings.HasPrefix(string(res.Stdout), want) {
					t.Errorf("stdout %q, want prefix %q", res.Stdout, want)
				}

				// A helper that returns without asking for the secret never
				// receives it, so sh cannot execute it.
				evil := "touch " + filepath.Join(h.dir, "EXECUTED")
				res, err = r.runInline(ctx(t), "a", `return 0`, nil, RunOptions{Secret: secret.FromBytes([]byte(evil))})
				if err != nil {
					t.Fatalf("%v: %s", err, res.Stderr)
				}
				if _, err := os.Stat(filepath.Join(h.dir, "EXECUTED")); err == nil {
					t.Fatal("unread secret was executed by sh")
				}
				for _, f := range []string{"PWNED", "PWNED2"} {
					if _, err := os.Stat(filepath.Join(h.dir, f)); err == nil {
						t.Fatalf("%s created: secret was interpreted", f)
					}
				}
				if bytes.Contains(res.Stdout, []byte("hunter2")) || bytes.Contains(res.Stderr, []byte("hunter2")) {
					t.Error("sudo password echoed")
				}
				wantAsked := 0
				if mode == "password" {
					wantAsked = 1
				}
				if len(p.asked) != wantAsked {
					t.Errorf("asked for the sudo password %d times, want %d", len(p.asked), wantAsked)
				}
			})
		}
	}
}

func TestSudoWrongPasswordThenRight(t *testing.T) {
	h := newFakeHost(t, "dash", "SHIM_SUDO=password", "SHIM_SUDO_PASSWORD=right")
	p := &countingPrompter{passwords: []string{"wrong", "right"}}
	r := newDirectRunner(map[string]Conn{"a": h}, p)
	if _, err := r.Probe(ctx(t), "a", "/nonexistent/stellar-core.cfg", "stellar-core"); err != nil {
		t.Fatal(err)
	}
	if want := []bool{false, true}; len(p.asked) != 2 || p.asked[0] != want[0] || p.asked[1] != want[1] {
		t.Errorf("asked %v, want %v", p.asked, want)
	}
	// Remembered for the session.
	if _, err := r.Probe(ctx(t), "a", "/nonexistent/stellar-core.cfg", "stellar-core"); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 2 {
		t.Error("asked again in the same session")
	}
}

func TestSudoGivesUp(t *testing.T) {
	h := newFakeHost(t, "dash", "SHIM_SUDO=password", "SHIM_SUDO_PASSWORD=right")
	p := &countingPrompter{passwords: []string{"a", "b", "c"}}
	_, err := newDirectRunner(map[string]Conn{"a": h}, p).Probe(ctx(t), "a", "/x/y.cfg", "stellar-core")
	var se *SudoError
	if !errors.As(err, &se) || !se.PasswordAsked {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "right") {
		t.Error("error mentions the password")
	}
}

func writeConfig(t *testing.T, h *fakeHost, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(h.dir, "etc", "stellar-core.cfg")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleConfig = `# managed by hand
NETWORK_PASSPHRASE="Test SDF Network ; September 2015"
NODE_SEED="` + testSeed + ` acme-a"
NODE_IS_VALIDATOR=true
HTTP_PORT=11626
BUCKET_DIR_PATH="buckets"
# stray copy in a comment: ` + testSeed + `
`

func TestReadConfigRedactsOnHost(t *testing.T) {
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			h := newFakeHost(t, shell)
			cfg := writeConfig(t, h, sampleConfig, 0o640)
			r := newDirectRunner(map[string]Conn{"a": h}, nil)
			rc, err := r.ReadConfig(ctx(t), "a", cfg)
			if err != nil {
				t.Fatal(err)
			}
			if rc.PublicKey != testAddress || rc.Seed != SeedPresent || rc.NodeAlias != "acme-a" {
				t.Errorf("header %+v", rc)
			}
			if bytes.Contains(rc.Text, []byte(testSeed)) {
				t.Fatalf("seed in output:\n%s", rc.Text)
			}
			wantLine := `NODE_SEED = "<redacted:` + testAddress + `> acme-a"`
			if !bytes.Contains(rc.Text, []byte(wantLine)) {
				t.Errorf("marker missing; got:\n%s", rc.Text)
			}
			if !bytes.Contains(rc.Text, []byte(`NETWORK_PASSPHRASE="Test SDF Network ; September 2015"`)) {
				t.Error("other lines changed")
			}
			if log := h.argvLog(t); strings.Contains(log, testSeed) || !strings.Contains(log, "sec-to-pub") {
				t.Errorf("stellar-core argv log: %q", log)
			}
		})
	}
}

func TestReadConfigWithoutSeed(t *testing.T) {
	h := newFakeHost(t, "dash")
	cfg := writeConfig(t, h, "NETWORK_PASSPHRASE=\"x\"\n", 0o640)
	rc, err := newDirectRunner(map[string]Conn{"a": h}, nil).ReadConfig(ctx(t), "a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Seed != SeedAbsent || rc.PublicKey != "" {
		t.Errorf("%+v", rc)
	}
}

func TestReadConfigBadSeed(t *testing.T) {
	h := newFakeHost(t, "dash")
	bad := "SBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	cfg := writeConfig(t, h, `NODE_SEED="`+bad+`"`+"\n", 0o640)
	rc, err := newDirectRunner(map[string]Conn{"a": h}, nil).ReadConfig(ctx(t), "a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rc.PublicKey != "" || bytes.Contains(rc.Text, []byte(bad)) || !bytes.Contains(rc.Text, []byte("<redacted:unknown>")) {
		t.Errorf("%+v\n%s", rc, rc.Text)
	}
}

func TestReadConfigMissing(t *testing.T) {
	h := newFakeHost(t, "dash")
	_, err := newDirectRunner(map[string]Conn{"a": h}, nil).ReadConfig(ctx(t), "a", filepath.Join(h.dir, "nope.cfg"))
	if !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v", err)
	}
}

func TestProbe(t *testing.T) {
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			h := newFakeHost(t, shell, "SHIM_NTP=no", "SHIM_ACTIVE=inactive")
			cfg := writeConfig(t, h, sampleConfig, 0o644)
			f, err := newDirectRunner(map[string]Conn{"a": h}, nil).Probe(ctx(t), "a", cfg, "stellar-core")
			if err != nil {
				t.Fatal(err)
			}
			if !f.CoreInstalled || f.CoreVersion != "stellar-core 30.0.0 (shim)" {
				t.Errorf("core: %+v", f)
			}
			if f.ServiceActive != "inactive" || f.ServiceEnabled != "enabled" || f.ServiceLoad != "loaded" {
				t.Errorf("service: %+v", f)
			}
			if !f.ConfigPresent || !f.ConfigReadable || f.ConfigMode != "644" || !f.WorldReadable() || len(f.ConfigSHA256) != 64 {
				t.Errorf("config: %+v", f)
			}
			if f.HTTPPort != 11626 || f.BucketDir != filepath.Join(h.dir, "buckets") {
				t.Errorf("port/bucket: %d %q", f.HTTPPort, f.BucketDir)
			}
			if f.DiskTotalKB <= 0 || f.DiskAvailKB <= 0 || f.NTPSync != "no" {
				t.Errorf("disk/ntp: %+v", f)
			}
		})
	}
}

func TestArgValidation(t *testing.T) {
	ok := []Arg{
		{ArgPath, "/etc/stellar/stellar-core.cfg"}, {ArgName, "stellar-core"}, {ArgName, "stellar-core@2"},
		{ArgAlias, "acme-a"}, {ArgPort, "11626"}, {ArgPort, "65535"}, {ArgWord, "keep"},
	}
	bad := []Arg{
		{ArgPath, "relative/path"}, {ArgPath, "/etc/../shadow"}, {ArgPath, "/a b"}, {ArgPath, "/a;b"},
		{ArgPath, "/a$(x)"}, {ArgPath, "/a'b"}, {ArgPath, "/./x"}, {ArgName, "-rf"}, {ArgName, "a b"},
		{ArgAlias, "a/b"}, {ArgPort, "0"}, {ArgPort, "65536"}, {ArgPort, "080"}, {ArgWord, "Keep"},
		{ArgKind(99), "x"}, {ArgPath, "/x\n/y"},
	}
	for _, a := range ok {
		if err := a.Validate(); err != nil {
			t.Errorf("%v rejected: %v", a, err)
		}
	}
	for _, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("%v accepted", a)
		}
	}
	_, err := newDirectRunner(nil, nil).Run(ctx(t), "a", HelperProbe, []Arg{{ArgPath, "/x;reboot"}}, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("Run with a bad arg: %v", err)
	}
}

func TestSudoCommandIsFixed(t *testing.T) {
	if got := sudoCommand(false, []string{"/etc/x", "it's"}); got != `sudo -n -- sh -s -- '/etc/x' 'it'\''s'` {
		t.Errorf("got %s", got)
	}
	if got := sudoCommand(true, nil); got != `sudo -k -S -p '' -- sh -s --` {
		t.Errorf("got %s", got)
	}
}

func FuzzQuote(f *testing.F) {
	for _, s := range []string{"", "a", "it's", `"$(x)"`, "a\nb", "'''", "\\"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if strings.ContainsRune(s, 0) {
			return // argv cannot carry NUL
		}
		out, err := exec.Command("sh", "-c", "printf '%s' "+Quote(s)).Output()
		if err != nil {
			t.Fatalf("sh failed for %q: %v", s, err)
		}
		if string(out) != s {
			t.Fatalf("Quote(%q) round-tripped to %q", s, out)
		}
	})
}

func TestProgressLines(t *testing.T) {
	h := newFakeHost(t, "dash")
	r := newDirectRunner(map[string]Conn{"a": h}, nil)
	var got []string
	w := &progressWriter{progress: func(s string) { got = append(got, s) }}
	io.WriteString(w, "@@progress step 1\nnoise\n@@prog")
	io.WriteString(w, "ress step 2 "+testSeed+"\npartial")
	w.flush()
	if len(got) != 2 || got[0] != "step 1" || strings.Contains(got[1], testSeed) {
		t.Errorf("progress %q", got)
	}
	if w.buf.String() != "noise\npartial" {
		t.Errorf("stderr %q", w.buf.String())
	}
	_ = r
}

// One full pass over real SSH: sshtest server -> fake host.
func TestRunOverSSH(t *testing.T) {
	h := newFakeHost(t, "dash")
	cfg := writeConfig(t, h, sampleConfig, 0o640)
	srv := sshtest.NewServer(t)
	client := sshtest.NewSigner(t)
	srv.AuthorizedKeys = []ssh.PublicKey{client.PublicKey()}
	srv.Exec = func(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
		err := h.Exec(context.Background(), cmd, stdin, stdout, stderr)
		var ee *sshx.ExitError
		if errors.As(err, &ee) {
			return ee.Status
		}
		if err != nil {
			return 255
		}
		return 0
	}
	srv.Start()
	c, err := ssh.Dial("tcp", srv.Addr, &ssh.ClientConfig{
		User:            "op",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(client)},
		HostKeyCallback: ssh.FixedHostKey(srv.HostKey.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := newDirectRunner(map[string]Conn{"a": &sshx.Conn{Client: c}}, nil)
	rc, err := r.ReadConfig(ctx(t), "a", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rc.PublicKey != testAddress || bytes.Contains(rc.Text, []byte(testSeed)) {
		t.Errorf("%+v", rc)
	}
	_, err = r.Run(ctx(t), "a", HelperReadConfig, []Arg{{ArgPath, filepath.Join(h.dir, "missing.cfg")}}, RunOptions{})
	var he *Error
	if !errors.As(err, &he) || he.Exit != 3 || !strings.Contains(he.StderrTail, "not found") {
		t.Errorf("err = %v", err)
	}
}
