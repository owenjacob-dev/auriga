// Package secrets is the secrets-leak suite (`make test-secrets`).
//
// It plants known fake secrets in every code path that exists so far, then
// scans every file written under temporary XDG directories and every byte of
// captured terminal output. Each milestone adds its paths here (SPEC.md
// "Security requirements": a test feeds known fake secrets through every code
// path and greps all outputs for them).
package secrets

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// Planted secrets. They are syntactically valid StrKey-shaped seeds so that
// pattern-based redaction is exercised, but they control nothing.
var planted = []string{
	"SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	"SBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
}

// env sets up throwaway XDG directories and returns the resolved dirs.
func env(t *testing.T) xdg.Dirs {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	d, err := xdg.Resolve(os.Getenv, "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// reporter is the part of testing.T the scanner needs.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// scan reports every planted secret found in any file under roots or in any
// of the captured outputs.
func scan(t reporter, roots []string, outputs ...[]byte) {
	t.Helper()
	check := func(where string, b []byte) {
		for _, s := range planted {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("planted secret %.4s… found in %s", s, where)
			}
		}
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("read %s: %v", p, err)
				return nil
			}
			check(p, b)
			return nil
		})
	}
	for i, out := range outputs {
		check(fmt.Sprintf("captured output #%d", i), out)
	}
}

// leakyScreen puts planted secrets in panic values on demand.
type leakyScreen struct{}

func (leakyScreen) Init() tea.Cmd { return nil }
func (s leakyScreen) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "x" {
		panic("NODE_SEED=" + planted[0] + " and " + planted[1])
	}
	return s, nil
}
func (leakyScreen) View(ui.Context) ui.Layout { return ui.Layout{Body: []string{"leaky"}} }
func (leakyScreen) Keys() []key.Binding       { return nil }
func (leakyScreen) Busy() string              { return "" }

// TestCrashPathDoesNotLeak drives a real program into a panic carrying
// secrets, writes the crash report and scans everything.
func TestCrashPathDoesNotLeak(t *testing.T) {
	dirs := env(t)
	crash := app.NewCrash(nil)
	m := app.New(app.Options{Version: "test", Crash: crash, Root: leakyScreen{}, DisableClock: true})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))
	tm.Send(tea.KeyPressMsg{Code: 'x', Text: "x"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	out, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil {
		t.Fatal(err)
	}
	if !crash.Happened() {
		t.Fatal("panic was not recorded")
	}
	if _, err := crash.WriteReport(dirs.State, "test"); err != nil {
		t.Fatal(err)
	}
	scan(t, []string{dirs.Config, dirs.State, os.Getenv("HOME")}, out, []byte(crash.Report("test")))
}

// TestScannerFindsLeaks proves the scanner itself works.
func TestScannerFindsLeaks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"+planted[1]+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	scan(r, []string{dir}, []byte(planted[0]))
	if r.found != 2 {
		t.Errorf("scanner found %d leaks, want 2", r.found)
	}
}

type recorder struct{ found int }

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.found++ }
