// Package app is the root Bubble Tea model: it owns the screen stack, the
// global keys, the help overlay, the quit confirmation and panic safety.
package app

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/dashboard"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// MinWidth and MinHeight are the smallest terminal every screen must fit
// (SPEC.md "TUI layout and keybindings").
const (
	MinWidth  = 80
	MinHeight = 24
)

// Options configure the root model.
type Options struct {
	Version string
	NoColor bool
	// Now returns the current time; tests pass a fixed clock.
	Now func() time.Time
	// Crash collects panics; nil creates a private one.
	Crash *Crash
	// Root is the home screen; nil means the dashboard.
	Root ui.Screen
	// DisableClock stops the once-a-second clock tick (tests).
	DisableClock bool
}

type overlay int

const (
	overlayNone overlay = iota
	overlayHelp
	overlayQuitConfirm
)

// Model is the root model.
type Model struct {
	opts    Options
	keys    globalKeys
	theme   theme.Theme
	dark    bool
	width   int
	height  int
	stack   []ui.Screen
	overlay overlay
	busy    string // reason shown in the quit confirmation
	crash   *Crash
}

// New builds the root model.
func New(opts Options) Model {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Crash == nil {
		opts.Crash = NewCrash(nil)
	}
	root := opts.Root
	if root == nil {
		root = dashboard.New()
	}
	m := Model{
		opts:  opts,
		keys:  newGlobalKeys(),
		dark:  true, // until the terminal answers the background query
		stack: []ui.Screen{root},
		crash: opts.Crash,
	}
	m.theme = theme.New(m.dark, opts.NoColor)
	return m
}

type tickMsg time.Time

func (m Model) tick() tea.Cmd {
	if m.opts.DisableClock {
		return nil
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return m.crash.safeCmd(tea.Batch(
		tea.RequestBackgroundColor,
		m.tick(),
		m.top().Init(),
	))
}

func (m Model) top() ui.Screen { return m.stack[len(m.stack)-1] }

// Push opens a screen on top of the stack.
func (m *Model) Push(s ui.Screen) tea.Cmd {
	m.stack = append(m.stack, s)
	return s.Init()
}

// pop closes the top screen; the home screen never closes.
func (m *Model) pop() {
	if len(m.stack) > 1 {
		m.stack = m.stack[:len(m.stack)-1]
	}
}

func (m Model) busyReason() string {
	for _, s := range m.stack {
		if r := s.Busy(); r != "" {
			return r
		}
	}
	return ""
}

// Update implements tea.Model. Panics are recorded and end the program.
func (m Model) Update(msg tea.Msg) (out tea.Model, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			m.crash.record(r)
			out, cmd = m, tea.Quit
		}
	}()
	next, cmd := m.update(msg)
	return next, m.crash.safeCmd(cmd)
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case crashMsg:
		return m, tea.Quit
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.theme = theme.New(m.dark, m.opts.NoColor)
		return m, nil
	case tickMsg:
		return m, m.tick()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	s, cmd := m.top().Update(msg)
	m.stack[len(m.stack)-1] = s
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case overlayQuitConfirm:
		switch {
		case key.Matches(msg, keyYes):
			return m, tea.Quit
		case key.Matches(msg, keyNo):
			m.overlay = overlayNone
		}
		return m, nil
	case overlayHelp:
		if key.Matches(msg, m.keys.Help, m.keys.Back) {
			m.overlay = overlayNone
			return m, nil
		}
		if key.Matches(msg, m.keys.Quit) {
			return m.quit()
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case key.Matches(msg, m.keys.Help):
		m.overlay = overlayHelp
		return m, nil
	}

	s, cmd := m.top().Update(msg)
	m.stack[len(m.stack)-1] = s
	if cmd == nil && key.Matches(msg, m.keys.Back) {
		m.pop()
	}
	return m, cmd
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	if r := m.busyReason(); r != "" {
		m.busy = r
		m.overlay = overlayQuitConfirm
		return m, nil
	}
	return m, tea.Quit
}

// View implements tea.Model.
func (m Model) View() (v tea.View) {
	defer func() {
		if r := recover(); r != nil {
			m.crash.record(r)
			v = tea.NewView("auriga stopped after an internal error; restoring the terminal…")
			v.AltScreen = true
		}
	}()
	v = tea.NewView(m.Render())
	v.AltScreen = true
	return v
}

// Render draws the current frame as a string. It is View without the
// tea.View wrapper, for tests.
func (m Model) Render() string {
	w, h := m.width, m.height
	if w == 0 || h == 0 {
		return ""
	}
	t := m.theme
	if w < MinWidth || h < MinHeight {
		msg := fmt.Sprintf("auriga needs a terminal of at least %d×%d; this one is %d×%d.", MinWidth, MinHeight, w, h)
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().Width(max(10, w-2)).Align(lipgloss.Center).Render(msg+"\n\nq quit"))
	}

	ctx := ui.Context{
		Theme: t,
		Cols:  w - components.FrameChromeCols,
		Rows:  h - components.FrameChromeRows,
		Now:   m.opts.Now(),
	}
	layout := m.top().View(ctx)
	title := t.Title.Render("auriga")
	if layout.Subtitle != "" {
		title += t.Muted.Render(" · " + layout.Subtitle)
	}
	spec := components.FrameSpec{
		Width: w, Height: h,
		Title:  title,
		Right:  t.Muted.Render(ctx.Now.UTC().Format("15:04:05") + " UTC"),
		Body:   layout.Body,
		Status: layout.Status,
		Footer: components.Hints(t, append(m.top().Keys(), m.keys.Help, m.keys.Quit)...),
	}
	switch m.overlay {
	case overlayHelp:
		spec.Body = m.helpBody(ctx)
		spec.Footer = components.Hints(t, m.keys.Back, m.keys.Quit)
	case overlayQuitConfirm:
		spec.Body = []string{
			"",
			t.Warn.Render("◐ " + m.busy),
			"",
			"Quit anyway? Work in progress on hosts will be interrupted.",
		}
		spec.Footer = components.Hints(t, keyYes, keyNo)
	}
	return components.Frame(t, spec)
}

// helpBody lists the keys for the current screen, then the global keys.
func (m Model) helpBody(ctx ui.Context) []string {
	t := ctx.Theme
	row := func(kb key.Binding, desc string) string {
		return "  " + t.Key.Render(components.Fit(strings.Join(kb.Keys(), ", "), 14)) + desc
	}
	lines := []string{t.Header.Render("Keys")}
	screenKeys := m.top().Keys()
	added := false
	for _, kb := range screenKeys {
		if kb.Enabled() {
			lines = append(lines, row(kb, kb.Help().Desc))
			added = true
		}
	}
	if added {
		lines = append(lines, "")
	}
	lines = append(lines,
		row(m.keys.Help, "Show or hide this help"),
		row(m.keys.Back, "Go back or close a dialog"),
		row(m.keys.Quit, "Quit (asks first while work is running on a host)"),
	)
	return lines
}
