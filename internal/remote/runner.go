package remote

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
)

//go:embed scripts/*.sh
var scripts embed.FS

// Helper names the embedded scripts.
type Helper string

// The helpers available so far. Later milestones add stage, install,
// restart, rollback and gen-seed.
const (
	HelperProbe      Helper = "probe"
	HelperReadConfig Helper = "read-config"
	HelperAdminGet   Helper = "admin-get"
	HelperBackups    Helper = "list-backups"
)

func script(h Helper) ([]byte, error) {
	return scripts.ReadFile("scripts/" + string(h) + ".sh")
}

// Result is the outcome of one helper run.
type Result struct {
	Helper   Helper
	Stdout   []byte
	Stderr   []byte // progress lines removed
	Exit     int
	Duration time.Duration
}

// Error is a helper that ran and failed (SPEC.md "Errors": helper name, exit
// code, last 20 lines of stderr).
type Error struct {
	Alias      string
	Helper     Helper
	Exit       int
	StderrTail string
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s: helper %s exited %d", e.Alias, e.Helper, e.Exit)
	if e.StderrTail != "" {
		msg += ":\n" + e.StderrTail
	}
	return msg
}

// SudoError means sudo could not be used on the host.
type SudoError struct {
	Alias         string
	PasswordAsked bool
	Detail        string
}

func (e *SudoError) Error() string {
	if e.PasswordAsked {
		return fmt.Sprintf("%s: sudo rejected the password (%s); check it, or add the sudoers rule from the README", e.Alias, e.Detail)
	}
	return fmt.Sprintf("%s: sudo is not available without a password (%s); add the sudoers rule from the README", e.Alias, e.Detail)
}

// Conn is what the runner needs from a host connection.
type Conn interface {
	Exec(ctx context.Context, cmd string, stdin io.Reader, stdout, stderr io.Writer) error
}

// Pool is the part of sshx.Pool the runner uses.
type Pool interface {
	Run(ctx context.Context, alias string, fn func(ctx context.Context, c *sshx.Conn) error) error
}

// PasswordPrompter asks the operator for a host's sudo password. The caller
// takes ownership of the buffer.
type PasswordPrompter interface {
	SudoPassword(ctx context.Context, alias string, retry bool) (*secret.Buffer, error)
}

type sudoMode int

const (
	sudoUnknown sudoMode = iota
	sudoNoPassword
	sudoPassword
)

type sudoState struct {
	// mu serialises discovery, so helpers that start together on a host ask
	// for its sudo password once, not once each.
	mu       sync.Mutex
	mode     sudoMode
	password *secret.Buffer
}

// Runner runs helpers on hosts through the pool. It remembers per host
// whether sudo needs a password, and the password for the session.
type Runner struct {
	pool     Pool
	prompter PasswordPrompter
	// exec runs one command on alias; it is the pool by default and a
	// direct connection in tests.
	exec func(ctx context.Context, alias, cmd string, stdin io.Reader, stdout, stderr io.Writer) error

	mu   sync.Mutex
	sudo map[string]*sudoState
}

// NewRunner returns a runner using pool for connections and prompter for
// sudo passwords.
func NewRunner(pool Pool, prompter PasswordPrompter) *Runner {
	r := &Runner{pool: pool, prompter: prompter, sudo: map[string]*sudoState{}}
	r.exec = func(ctx context.Context, alias, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
		return pool.Run(ctx, alias, func(ctx context.Context, c *sshx.Conn) error {
			return c.Exec(ctx, cmd, stdin, stdout, stderr)
		})
	}
	return r
}

// newDirectRunner runs commands on fixed connections (tests).
func newDirectRunner(conns map[string]Conn, prompter PasswordPrompter) *Runner {
	r := &Runner{prompter: prompter, sudo: map[string]*sudoState{}}
	r.exec = func(ctx context.Context, alias, cmd string, stdin io.Reader, stdout, stderr io.Writer) error {
		c, ok := conns[alias]
		if !ok {
			return fmt.Errorf("%s: %w", alias, sshx.ErrUnknownHost)
		}
		return c.Exec(ctx, cmd, stdin, stdout, stderr)
	}
	return r
}

// Close zeroes cached sudo passwords.
func (r *Runner) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sudo {
		s.password.Zero()
	}
	r.sudo = map[string]*sudoState{}
}

// Forget drops a host's sudo state (host removed from the inventory).
func (r *Runner) Forget(alias string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.sudo[alias]; s != nil {
		s.password.Zero()
		delete(r.sudo, alias)
	}
}

const maxSudoTries = 3

// ensureSudo finds out whether alias needs a sudo password and, if so,
// obtains a working one (SPEC.md: probe with sudo -n, then prompt once per
// host per session).
func (r *Runner) ensureSudo(ctx context.Context, alias string) (*sudoState, error) {
	r.mu.Lock()
	st := r.sudo[alias]
	if st == nil {
		st = &sudoState{}
		r.sudo[alias] = st
	}
	r.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mode != sudoUnknown {
		return st, nil
	}
	// The probe is exactly the form helpers run with, so a sudoers rule that
	// allows "sh" is detected correctly.
	var stderr bytes.Buffer
	err := r.exec(ctx, alias, sudoCommand(false, nil), strings.NewReader("exit 0\n"), io.Discard, &stderr)
	if err == nil {
		st.mode = sudoNoPassword
		return st, nil
	}
	var ee *sshx.ExitError
	if !errors.As(err, &ee) {
		return nil, err
	}
	if r.prompter == nil {
		return nil, &SudoError{Alias: alias, Detail: tail(stderr.Bytes(), 2)}
	}
	for attempt := range maxSudoTries {
		pw, err := r.prompter.SudoPassword(ctx, alias, attempt > 0)
		if err != nil {
			return nil, err
		}
		stdin := passwordThen(pw, []byte("exit 0\n"))
		stderr.Reset()
		err = r.exec(ctx, alias, sudoCommand(true, nil), bytes.NewReader(stdin), io.Discard, &stderr)
		clear(stdin)
		if err == nil {
			st.mode, st.password = sudoPassword, pw
			return st, nil
		}
		pw.Zero()
		if !errors.As(err, &ee) {
			return nil, err
		}
	}
	return nil, &SudoError{Alias: alias, PasswordAsked: true, Detail: secret.Redact(tail(stderr.Bytes(), 2))}
}

// sudoCommand is the only command line Auriga ever sends. With a password,
// -k makes sudo ignore cached credentials so it always reads the password
// line from stdin; otherwise a cached credential would leave the password
// for sh to execute as a command.
func sudoCommand(withPassword bool, args []string) string {
	var b strings.Builder
	if withPassword {
		b.WriteString("sudo -k -S -p '' -- sh -s --")
	} else {
		b.WriteString("sudo -n -- sh -s --")
	}
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(Quote(a))
	}
	return b.String()
}

// passwordThen builds stdin: the sudo password line, then rest. The caller
// clears the result.
func passwordThen(pw *secret.Buffer, rest []byte) []byte {
	out := make([]byte, 0, pw.Len()+1+len(rest))
	out = append(out, pw.Bytes()...)
	out = append(out, '\n')
	return append(out, rest...)
}

// coreFuncs is shell shared by every helper. auriga_core runs stellar-core
// as the stellar user from the service's working directory, so relative
// DATABASE and BUCKET_DIR_PATH values resolve exactly as they do for the
// running node. The packaged unit sets no WorkingDirectory, which systemd
// runs from "/". Commands that read no config (gen-seed, sec-to-pub) do not
// need it.
const coreFuncs = `auriga_workdir() {
	_wd=$(systemctl show -p WorkingDirectory --value "$1" 2>/dev/null)
	_wd=${_wd#-}
	case "$_wd" in
	"" ) _wd=/ ;;
	"~") _wd=$(getent passwd stellar | cut -d: -f6); [ -n "$_wd" ] || _wd=/ ;;
	esac
	printf '%s' "$_wd"
}
auriga_core() {
	_svc=$1; shift
	( cd "$(auriga_workdir "$_svc")" && sudo -n -u stellar -- stellar-core "$@" )
}
`

// secretRequest is the line a helper prints (via auriga_read_secret) when it
// is ready to read the secret from stdin.
const secretRequest = "@@ready-secret"

// wrap turns a helper body into a script sh parses completely before running
// it: the body becomes a function, and the line that calls it also exits.
//
// Secrets are not appended to the script. dash reads a piped script in large
// blocks, so it would swallow anything that follows. Instead the helper calls
// auriga_read_secret, which prints secretRequest and then reads one line; the
// runner writes the secret only when it sees that request, after sh has
// finished reading the script. A helper that never asks never receives it.
func wrap(body []byte) []byte {
	var b bytes.Buffer
	b.WriteString("LC_ALL=C; export LC_ALL\n")
	b.WriteString("auriga_read_secret() { printf '%s\\n' '" + secretRequest + "'; IFS= read -r \"$1\"; }\n")
	b.WriteString(coreFuncs)
	b.WriteString("auriga_main() {\n")
	b.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("}\nauriga_main \"$@\"; exit $?\n")
	return b.Bytes()
}

// RunOptions are optional inputs to Run.
type RunOptions struct {
	// Secret is sent, followed by a newline, when the helper calls
	// auriga_read_secret. It is not modified.
	Secret *secret.Buffer
	// Progress receives each "@@progress" line the helper prints to stderr.
	Progress func(line string)
}

// Run runs helper h on alias with args. It returns an *Error if the helper
// exits non-zero.
func (r *Runner) Run(ctx context.Context, alias string, h Helper, args []Arg, opt RunOptions) (*Result, error) {
	body, err := script(h)
	if err != nil {
		return nil, fmt.Errorf("unknown helper %q", h)
	}
	argv := make([]string, len(args))
	for i, a := range args {
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("%s: helper %s: %w", alias, h, err)
		}
		argv[i] = a.Value
	}
	st, err := r.ensureSudo(ctx, alias)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	stdout, stderr, err := r.execFramed(ctx, alias, st, body, argv, opt)
	res := &Result{Helper: h, Stdout: stdout, Stderr: stderr, Duration: time.Since(start)}
	var ee *sshx.ExitError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &ee):
		res.Exit = ee.Status
		return res, &Error{Alias: alias, Helper: h, Exit: ee.Status, StderrTail: secret.Redact(tail(res.Stderr, 20))}
	default:
		return res, err
	}
}

// execFramed sends sudo password (if any) and script on stdin, and the
// secret only when the helper requests it.
func (r *Runner) execFramed(ctx context.Context, alias string, st *sudoState, body []byte, argv []string, opt RunOptions) ([]byte, []byte, error) {
	pr, pw := io.Pipe()
	stdout := &secretRequestWriter{}
	if opt.Secret != nil {
		stdout.onRequest = func() {
			msg := append(append([]byte(nil), opt.Secret.Bytes()...), '\n')
			pw.Write(msg) //nolint:errcheck,gosec // a failed write surfaces as the helper's error
			clear(msg)
			pw.Close() //nolint:errcheck,gosec
		}
	}
	stderr := &progressWriter{progress: opt.Progress}

	go func() {
		var head []byte
		if st.mode == sudoPassword {
			head = passwordThen(st.password, nil)
		}
		head = append(head, wrap(body)...)
		_, err := pw.Write(head)
		clear(head)
		if err != nil || opt.Secret == nil {
			pw.Close() //nolint:errcheck,gosec
		}
		// With a secret, stdin stays open until the helper asks for it or
		// exits (the pipe is closed below).
	}()

	err := r.exec(ctx, alias, sudoCommand(st.mode == sudoPassword, argv), pr, stdout, stderr)
	pr.Close() //nolint:errcheck,gosec // unblocks the writer if the helper never read stdin
	pw.Close() //nolint:errcheck,gosec
	stdout.flush()
	stderr.flush()
	return stdout.buf.Bytes(), stderr.buf.Bytes(), err
}

// secretRequestWriter passes stdout through, removing the secret request
// line and calling onRequest the first time it appears.
type secretRequestWriter struct {
	onRequest func()
	asked     bool
	partial   []byte
	buf       bytes.Buffer
}

func (w *secretRequestWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := w.partial[:i+1]
		if string(line) == secretRequest+"\n" {
			if !w.asked && w.onRequest != nil {
				w.asked = true
				w.onRequest()
			}
		} else {
			w.buf.Write(line)
		}
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

func (w *secretRequestWriter) flush() {
	w.buf.Write(w.partial)
	w.partial = nil
}

// progressWriter splits stderr into "@@progress" lines and the rest.
type progressWriter struct {
	progress func(string)
	partial  []byte
	buf      bytes.Buffer
}

const progressPrefix = "@@progress "

func (w *progressWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		w.line(w.partial[:i+1])
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

func (w *progressWriter) line(l []byte) {
	if rest, ok := bytes.CutPrefix(l, []byte(progressPrefix)); ok {
		if w.progress != nil {
			w.progress(secret.Redact(strings.TrimRight(string(rest), "\n")))
		}
		return
	}
	w.buf.Write(l)
}

func (w *progressWriter) flush() {
	if len(w.partial) > 0 {
		w.line(w.partial)
		w.partial = nil
	}
}

// tail returns the last n lines of b.
func tail(b []byte, n int) string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
