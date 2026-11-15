package app

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
)

var fixedNow = func() time.Time { return time.Date(2026, 11, 14, 14, 2, 11, 0, time.UTC) }

func newTestModel(noColor bool, root ui.Screen) Model {
	return New(Options{Version: "test", NoColor: noColor, Now: fixedNow, Root: root, DisableClock: true})
}

func send(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	var tm tea.Model = m
	for _, msg := range msgs {
		tm, _ = tm.Update(msg)
	}
	return tm.(Model)
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

var size80x24 = tea.WindowSizeMsg{Width: 80, Height: 24}

// colourSGR matches SGR sequences that set a colour.
var colourSGR = regexp.MustCompile(`\x1b\[[0-9;]*(3[0-9]|4[0-9]|9[0-7]|10[0-7])(;[0-9;]*)?m`)

func assertFrameSize(t *testing.T, out string, w, h int) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) != h {
		t.Fatalf("%d lines, want %d", len(lines), h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != w {
			t.Errorf("line %d is %d wide, want %d", i, lw, w)
		}
	}
}

// Snapshot tests compare the plain-text frame (escape codes stripped) with a
// golden file. Update deliberately with: go test ./internal/app -update
func TestSnapshots(t *testing.T) {
	cases := map[string][]tea.Msg{
		"dashboard":      {size80x24},
		"help":           {size80x24, keyPress("?")},
		"too_small":      {tea.WindowSizeMsg{Width: 60, Height: 20}},
		"dashboard_wide": {tea.WindowSizeMsg{Width: 120, Height: 30}},
	}
	for name, msgs := range cases {
		for _, noColor := range []bool{false, true} {
			variant := name
			if noColor {
				variant += "_nocolor"
			}
			t.Run(variant, func(t *testing.T) {
				out := send(t, newTestModel(noColor, nil), msgs...).Render()
				if noColor && colourSGR.MatchString(out) {
					t.Errorf("NO_COLOR frame contains colour sequences")
				}
				if name != "too_small" {
					ws := msgs[0].(tea.WindowSizeMsg)
					assertFrameSize(t, out, ws.Width, ws.Height)
				}
				golden.RequireEqual(t, ansi.Strip(out))
			})
		}
	}
}

func TestHelpToggle(t *testing.T) {
	m := send(t, newTestModel(true, nil), size80x24, keyPress("?"))
	if m.overlay != overlayHelp {
		t.Fatal("? did not open help")
	}
	if m2 := send(t, m, keyPress("?")); m2.overlay != overlayNone {
		t.Error("? did not close help")
	}
	if m2 := send(t, m, keyPress("esc")); m2.overlay != overlayNone {
		t.Error("esc did not close help")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := newTestModel(true, nil).Update(keyPress(k))
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s did not quit", k)
		}
	}
}

// busyScreen reports work in progress so quitting asks first.
type busyScreen struct{ fakeScreen }

func (busyScreen) Busy() string { return "Deploy running on acme-b" }

type fakeScreen struct{}

func (fakeScreen) Init() tea.Cmd                         { return nil }
func (f fakeScreen) Update(tea.Msg) (ui.Screen, tea.Cmd) { return f, nil }
func (fakeScreen) View(ui.Context) ui.Layout             { return ui.Layout{Body: []string{"fake"}} }
func (fakeScreen) Keys() []key.Binding                   { return nil }
func (fakeScreen) Busy() string                          { return "" }

func TestQuitAsksWhileBusy(t *testing.T) {
	m := send(t, newTestModel(true, busyScreen{}), size80x24)
	tm, cmd := m.Update(keyPress("q"))
	m = tm.(Model)
	if cmd != nil || m.overlay != overlayQuitConfirm {
		t.Fatal("quit did not ask for confirmation")
	}
	golden.RequireEqual(t, ansi.Strip(m.Render()))

	if m2 := send(t, m, keyPress("n")); m2.overlay != overlayNone {
		t.Error("n did not cancel")
	}
	_, cmd = m.Update(keyPress("y"))
	if cmd == nil {
		t.Fatal("y did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("y did not quit")
	}
}

func TestBackgroundColourSwitchesTheme(t *testing.T) {
	m := newTestModel(false, nil)
	if !m.theme.Dark {
		t.Fatal("default should be dark")
	}
	m = send(t, m, tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if m.theme.Dark {
		t.Error("white background should select the light theme")
	}
}

// Scripted key sequence through a real tea.Program.
func TestProgramKeySequence(t *testing.T) {
	tm := teatest.NewTestModel(t, newTestModel(true, nil), teatest.WithInitialTermSize(80, 24))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("No hosts yet."))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(keyPress("?"))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte("Show or hide this help"))
	}, teatest.WithDuration(3*time.Second))
	tm.Send(keyPress("esc"))
	tm.Send(keyPress("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	if _, err := io.ReadAll(tm.FinalOutput(t)); err != nil {
		t.Fatal(err)
	}
}

// --- panic safety ---------------------------------------------------------

const fakeSeed = "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type panicScreen struct {
	fakeScreen
	inView bool
}

func (p panicScreen) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		panic("boom " + fakeSeed)
	}
	return p, nil
}

func (p panicScreen) View(ui.Context) ui.Layout {
	if p.inView {
		panic("view boom " + fakeSeed)
	}
	return ui.Layout{}
}

func TestPanicInUpdateIsRecordedAndQuits(t *testing.T) {
	crash := NewCrash(nil)
	m := New(Options{Now: fixedNow, Root: panicScreen{}, Crash: crash, DisableClock: true})
	_, cmd := m.Update(keyPress("x"))
	if !crash.Happened() {
		t.Fatal("panic not recorded")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("program did not quit after a panic")
	}
	assertRedactedReport(t, crash)
}

func TestPanicInViewIsRecorded(t *testing.T) {
	quit := make(chan struct{})
	crash := NewCrash(func() { close(quit) })
	m := New(Options{Now: fixedNow, Root: panicScreen{inView: true}, Crash: crash, DisableClock: true})
	m = send(t, m, size80x24)
	v := m.View()
	if !strings.Contains(v.Content, "internal error") {
		t.Errorf("view after panic = %q", v.Content)
	}
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("onCrash not called")
	}
	assertRedactedReport(t, crash)
}

func TestPanicInCommandBecomesCrashMsg(t *testing.T) {
	crash := NewCrash(nil)
	boom := func() tea.Msg { panic("cmd boom " + fakeSeed) }
	ok := func() tea.Msg { return nil }
	batch := crash.safeCmd(tea.Batch(ok, boom))
	msgs := batch().(tea.BatchMsg)
	var gotCrash bool
	for _, c := range msgs {
		if _, isCrash := c().(crashMsg); isCrash {
			gotCrash = true
		}
	}
	if !gotCrash || !crash.Happened() {
		t.Fatal("panicking command not converted")
	}
	m := New(Options{Now: fixedNow, Crash: crash, DisableClock: true})
	_, cmd := m.Update(crashMsg{})
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("crashMsg did not quit")
	}
	assertRedactedReport(t, crash)
}

func assertRedactedReport(t *testing.T, crash *Crash) {
	t.Helper()
	dir := t.TempDir()
	path, err := crash.WriteReport(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(fakeSeed)) {
		t.Error("crash report contains the seed")
	}
	if !bytes.Contains(b, []byte("boom")) {
		t.Errorf("crash report missing panic value: %s", b)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("crash report mode %o", fi.Mode().Perm())
	}
}
