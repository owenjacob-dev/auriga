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

	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/dashboard"
	"github.com/owenjacob-dev/auriga/internal/ui/hostdetail"
	"github.com/owenjacob-dev/auriga/internal/ui/hostpicker"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// MinWidth and MinHeight are the smallest terminal every screen must fit
// (SPEC.md "TUI layout and keybindings").
const (
	MinWidth  = 80
	MinHeight = 24
)

func newTheme(dark, noColor bool) theme.Theme { return theme.New(dark, noColor) }

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
	// Backend connects to hosts; nil runs the UI without hosts (tests).
	Backend Backend
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

	backend   Backend
	inv       *inventory.Inventory
	invBroken bool // the inventory file could not be read; never overwrite it
	fleet     *ui.Fleet
	prompts   []sshx.PromptEvent
	secretIn  *components.SecretInput
	probeSeq  int
	probing   map[string]int // alias -> seq of the probe in flight
	pollGen   map[string]int // alias -> current poll cycle
	// themeFixed is set when settings.toml chooses dark or light, so the
	// terminal's background answer is ignored.
	themeFixed bool
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

		backend:  opts.Backend,
		fleet:    &ui.Fleet{},
		secretIn: components.NewSecretInput(),
		probing:  map[string]int{},
		pollGen:  map[string]int{},
	}
	m.theme = newTheme(m.dark, opts.NoColor)
	if fa, ok := root.(ui.FleetAware); ok {
		fa.SetFleet(m.fleet)
	}
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
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.tick(), m.top().Init()}
	if m.backend != nil {
		cmds = append(cmds, m.listen(), m.loadSettings(), m.loadInventory())
	}
	return m.crash.safeCmd(tea.Batch(cmds...))
}

func (m Model) top() ui.Screen { return m.stack[len(m.stack)-1] }

// Push opens a screen on top of the stack.
func (m *Model) Push(s ui.Screen) tea.Cmd {
	if fa, ok := s.(ui.FleetAware); ok {
		fa.SetFleet(m.fleet)
	}
	m.stack = append(m.stack, s)
	return s.Init()
}

// Close releases the backend: connections closed, cached secrets zeroed,
// and any typed but unsent secret cleared.
func (m Model) Close() {
	m.secretIn.Clear()
	for _, p := range m.prompts {
		p.Cancel()
	}
	if m.backend != nil {
		m.backend.Close()
	}
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
		if !m.themeFixed {
			m.dark = msg.IsDark()
			m.theme = newTheme(m.dark, m.opts.NoColor)
		}
		return m, nil
	case tickMsg:
		return m, m.tick()
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if p := m.activePrompt(); p != nil && p.HostKey == nil {
			m.secretIn.Update(msg)
			return m, nil
		}
	case backendDoneMsg:
		return m, nil
	case ui.CloseMsg:
		m.pop()
		return m, nil
	case ui.OpenHostPickerMsg:
		var existing []string
		for _, h := range m.fleet.Hosts {
			existing = append(existing, h.Alias)
		}
		return m, m.Push(hostpicker.New(existing))
	case ui.OpenHostMsg:
		return m, m.Push(hostdetail.New(msg.Alias).WithVersion(m.opts.Version))
	}
	if next, cmd, handled := m.handleFleet(msg); handled {
		return next, cmd
	}
	if next, cmd, handled := m.handlePoll(msg); handled {
		return next, cmd
	}
	s, cmd := m.top().Update(msg)
	m.stack[len(m.stack)-1] = s
	return m, cmd
}

func (m Model) activePrompt() *sshx.PromptEvent {
	if len(m.prompts) == 0 {
		return nil
	}
	return &m.prompts[0]
}

// nextPrompt removes the answered prompt.
func (m *Model) nextPrompt() {
	m.prompts = m.prompts[1:]
	m.secretIn.Clear()
}

func (m Model) handlePromptKey(p *sshx.PromptEvent, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if p.HostKey != nil {
		switch msg.String() {
		case "t":
			p.AnswerHostKey(sshx.HostKeyTrustSession)
		case "s":
			p.AnswerHostKey(sshx.HostKeyTrustAndSave)
		case "r", "esc":
			p.AnswerHostKey(sshx.HostKeyReject)
		default:
			return m, nil
		}
		m.nextPrompt()
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEnter:
		if m.secretIn.Len() == 0 {
			return m, nil
		}
		p.AnswerPassphrase(m.secretIn.Take())
		m.nextPrompt()
	case tea.KeyEscape:
		p.Cancel()
		m.nextPrompt()
	default:
		m.secretIn.Update(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if p := m.activePrompt(); p != nil && m.overlay != overlayQuitConfirm {
		return m.handlePromptKey(p, msg)
	}
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

	capturing := false
	if tc, ok := m.top().(ui.TextCapture); ok {
		capturing = tc.CapturingText()
	}
	if !capturing {
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m.quit()
		case key.Matches(msg, m.keys.Help):
			m.overlay = overlayHelp
			return m, nil
		}
	}
	s, cmd := m.top().Update(msg)
	m.stack[len(m.stack)-1] = s
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
		Fleet: m.fleet,
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
	if p := m.activePrompt(); p != nil && m.overlay != overlayQuitConfirm {
		spec.Body, spec.Footer = m.promptBody(ctx, p)
		return components.Frame(t, spec)
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

// promptBody renders the oldest pending prompt.
func (m Model) promptBody(ctx ui.Context, p *sshx.PromptEvent) ([]string, string) {
	t := ctx.Theme
	more := ""
	if n := len(m.prompts) - 1; n > 0 {
		more = t.Muted.Render(fmt.Sprintf("  (%d more waiting)", n))
	}
	if q := p.HostKey; q != nil {
		who := fmt.Sprintf("%s (%s)", q.Alias, q.Address)
		if q.Alias != p.Alias {
			who = fmt.Sprintf("%s (%s, jump host for %s)", q.Alias, q.Address, p.Alias)
		}
		body := []string{
			t.Warn.Render("◐ Unknown host key") + more,
			"",
			who + " presented a host key",
			"that is not in known_hosts:",
			"",
			"  " + t.Header.Render(q.KeyType+" "+q.Fingerprint),
			"",
			"Check it against the host before trusting it, for example on the host:",
			t.Muted.Render("  ssh-keygen -lf /etc/ssh/ssh_host_" + strings.TrimPrefix(q.KeyType, "ssh-") + "_key.pub"),
		}
		if q.SaveTo != "" {
			body = append(body, "", t.Muted.Render("Trust and save appends it to "+q.SaveTo+"."))
		}
		footer := components.Hints(t,
			key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "trust this session")),
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "trust and save")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reject")),
		)
		return body, footer
	}
	title := fmt.Sprintf("Passphrase for %s (%s)", p.KeyPath, p.Alias)
	if p.Sudo {
		title = "sudo password for " + p.Alias
	}
	body := []string{t.Header.Render(title) + more, ""}
	if p.Retry {
		body = append(body, t.Bad.Render("○ That did not work. Try again."), "")
	}
	body = append(body,
		"  "+m.secretIn.View(ctx.Cols-4),
		"",
		t.Muted.Render("Kept in memory for this session only and never written to disk."),
	)
	footer := components.Hints(t,
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "submit")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	)
	return body, footer
}

// helpBody lists the keys for the current screen, then the global keys.
func (m Model) helpBody(ctx ui.Context) []string {
	t := ctx.Theme
	row := func(kb key.Binding, desc string) string {
		return "  " + t.Key.Render(components.Fit(strings.Join(kb.Keys(), ", "), 14)) + desc
	}
	screenRow := func(kb key.Binding) string {
		return "  " + t.Key.Render(components.Fit(kb.Help().Key, 14)) + kb.Help().Desc
	}
	lines := []string{t.Header.Render("Keys")}
	screenKeys := m.top().Keys()
	if fh, ok := m.top().(ui.FullHelp); ok {
		screenKeys = fh.FullHelp()
	}
	added := false
	for _, kb := range screenKeys {
		if kb.Enabled() {
			lines = append(lines, screenRow(kb))
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
