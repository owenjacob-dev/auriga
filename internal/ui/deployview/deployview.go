// Package deployview is the deploy progress screen (SPEC.md "Screens" 5):
// a confirmation listing what will happen on each host, then a per-host
// step list with live output and the sync gate, then the outcome.
//
// The screen does no I/O: it asks the root model to start the deploy
// (StartMsg) and receives the coordinator's events as messages.
package deployview

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// Host is what the confirmation shows about one host.
type Host struct {
	Alias   string
	Running bool
	New     bool   // no config yet
	Seed    string // "keep the existing seed", "generate on the host", …
	Inits   []string
	Changes int // changed lines against the current config
}

// Request opens the screen.
type Request struct {
	Org, Network string
	Hosts        []Host
	// AllowParallel offers "all at once" (testnet, or no host running).
	AllowParallel bool
}

// Messages between the screen and the root model.
type (
	// StartMsg asks the root model to start the deploy.
	StartMsg struct{ Parallel bool }
	// EventMsg is one coordinator event.
	EventMsg struct{ Event deploy.Event }
	// DoneMsg ends the deploy.
	DoneMsg struct {
		Err      error
		Outcomes map[string]deploy.Outcome
	}
	// StopWaitingMsg ends alias's sync gate.
	StopWaitingMsg struct{ Alias string }
)

type phase int

const (
	phaseConfirm phase = iota
	phaseRunning
	phaseDone
)

type stepState struct {
	status deploy.Status
	detail string
}

type hostState struct {
	Host
	steps   map[deploy.Step]stepState
	sync    *deploy.Sync
	outcome deploy.Outcome
	current deploy.Step
}

// Model is the screen.
type Model struct {
	req      Request
	phase    phase
	parallel bool
	typed    string
	hosts    []*hostState
	selected int
	follow   bool // the selection follows the host being deployed
	notes    []string
	err      error
	scroll   int
}

// New opens the confirmation for req.
func New(req Request) *Model {
	m := &Model{req: req, follow: true}
	for _, h := range req.Hosts {
		m.hosts = append(m.hosts, &hostState{Host: h, steps: map[deploy.Step]stepState{}})
	}
	return m
}

// Init implements ui.Screen.
func (m *Model) Init() tea.Cmd { return nil }

// Busy implements ui.Screen.
func (m *Model) Busy() string {
	if m.phase == phaseRunning {
		return "A deploy is running on " + m.req.Org + "; quitting now can leave a host mid-step."
	}
	return ""
}

// CapturingText implements ui.TextCapture while a confirmation word is typed.
func (m *Model) CapturingText() bool { return m.phase == phaseConfirm && m.word() != "" }

// word is what the operator must type: "pubnet" on pubnet, "deploy" when
// the plan has a step a rollback cannot undo, otherwise nothing.
func (m *Model) word() string {
	if m.req.Network == "pubnet" {
		return "pubnet"
	}
	for _, h := range m.req.Hosts {
		if len(h.Inits) > 0 && (h.Running || !h.New) {
			return "deploy"
		}
	}
	return ""
}

var (
	kStart = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "deploy"))
	kMode  = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "one at a time / all at once"))
	kBack  = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kHost  = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next host"))
	kWait  = key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "stop waiting for sync"))
	kClose = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	switch m.phase {
	case phaseConfirm:
		keys := []key.Binding{kStart}
		if m.req.AllowParallel {
			keys = append(keys, kMode)
		}
		return append(keys, kBack)
	case phaseRunning:
		keys := []key.Binding{kHost}
		if h := m.host(); h != nil && h.current == deploy.StepSync && h.steps[deploy.StepSync].status == deploy.Running {
			keys = append(keys, kWait)
		}
		return keys
	}
	return []key.Binding{kHost, kClose}
}

func (m *Model) host() *hostState {
	if len(m.hosts) == 0 {
		return nil
	}
	return m.hosts[m.selected%len(m.hosts)]
}

func (m *Model) find(alias string) (int, *hostState) {
	for i, h := range m.hosts {
		if h.Alias == alias {
			return i, h
		}
	}
	return -1, nil
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case EventMsg:
		m.event(msg.Event)
	case DoneMsg:
		m.phase, m.err = phaseDone, msg.Err
		for alias, o := range msg.Outcomes {
			if _, h := m.find(alias); h != nil {
				h.outcome = o
			}
		}
	case tea.PasteMsg:
		if m.CapturingText() {
			m.typed += msg.Content
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Model) event(e deploy.Event) {
	if e.Host == "" {
		m.notes = append(m.notes, e.Detail)
		return
	}
	i, h := m.find(e.Host)
	if h == nil {
		return
	}
	if e.Outcome != "" {
		h.outcome = e.Outcome
	}
	if e.Step == "" {
		if e.Status == deploy.Warned {
			m.notes = append(m.notes, e.Host+": "+e.Detail)
		}
		return
	}
	h.steps[e.Step] = stepState{e.Status, e.Detail}
	if e.Sync != nil {
		h.sync = e.Sync
	}
	if e.Status == deploy.Running {
		h.current = e.Step
		if m.follow {
			m.selected = i
		}
	}
}

func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	s := msg.String()
	switch m.phase {
	case phaseConfirm:
		switch {
		case s == "esc":
			return ui.Emit(ui.CloseMsg{})
		case s == "enter":
			if w := m.word(); w != "" && strings.TrimSpace(m.typed) != w {
				return nil
			}
			m.phase = phaseRunning
			return ui.Emit(StartMsg{Parallel: m.parallel})
		case s == "tab" && m.req.AllowParallel:
			m.parallel = !m.parallel
		case s == "backspace" && m.typed != "":
			_, n := utf8.DecodeLastRuneInString(m.typed)
			m.typed = m.typed[:len(m.typed)-n]
		case m.CapturingText() && msg.Text != "":
			m.typed += msg.Text
		}
	case phaseRunning, phaseDone:
		switch s {
		case "tab":
			m.selected = (m.selected + 1) % max(1, len(m.hosts))
			m.follow, m.scroll = false, 0
		case "w":
			if h := m.host(); h != nil && m.phase == phaseRunning && h.current == deploy.StepSync {
				return ui.Emit(StopWaitingMsg{Alias: h.Alias})
			}
		case "down", "j":
			m.scroll++
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		case "esc":
			if m.phase == phaseDone {
				return ui.Emit(ui.CloseMsg{})
			}
		}
	}
	return nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	l := ui.Layout{Subtitle: "deploy " + m.req.Org + " · " + m.req.Network}
	var body []string
	switch m.phase {
	case phaseConfirm:
		body = m.confirmView(t, ctx.Cols)
		l.Status = "Nothing has changed on any host yet."
	default:
		body = m.progressView(t, ctx)
		l.Status = m.status()
	}
	if m.scroll > 0 && m.scroll < len(body) {
		body = body[m.scroll:]
	}
	for i := range body {
		body[i] = components.Fit(body[i], ctx.Cols)
	}
	if len(body) > ctx.Rows {
		body = body[:ctx.Rows]
	}
	l.Body = body
	return l
}

func (m *Model) mode() string {
	if m.parallel {
		return "all at once"
	}
	return "one at a time; each host must reach Synced! before the next starts"
}

func (m *Model) confirmView(t theme.Theme, cols int) []string {
	out := []string{
		t.Header.Render(fmt.Sprintf("Deploy %s to %d host(s) on %s", m.req.Org, len(m.hosts), m.req.Network)),
		"Order: " + m.mode(),
		"",
	}
	irreversible := false
	for _, h := range m.hosts {
		what := "adopted"
		switch {
		case h.New:
			what = "new node"
		case h.Running:
			what = "running"
		}
		line := fmt.Sprintf("  %-14s %s · %s", h.Alias, what, h.Seed)
		if !h.New {
			line += fmt.Sprintf(" · %d changed lines", h.Changes)
		}
		out = append(out, line)
		for _, in := range h.Inits {
			out = append(out, t.Warn.Render("    ◐ "+in+": a rollback cannot undo this"))
			irreversible = true
		}
	}
	out = append(out, "",
		t.Muted.Render("Per host: preflight, stage, verify, back up, install, init, restart,"),
		t.Muted.Render("then 60 s to come back healthy, or the backup is restored."))
	if irreversible {
		out = append(out, t.Muted.Render("Init steps run with the service stopped."))
	}
	out = append(out, "")
	if w := m.word(); w != "" {
		box := m.typed
		if box == "" {
			box = t.Muted.Render("…")
		}
		out = append(out, fmt.Sprintf("Type %s and press ⏎ to deploy: %s", t.Header.Render(w), box))
		if strings.TrimSpace(m.typed) != "" && !strings.HasPrefix(w, strings.TrimSpace(m.typed)) {
			out = append(out, t.Bad.Render("○ that is not "+w))
		}
	} else {
		out = append(out, "Press ⏎ to deploy.")
	}
	return out
}

func symbol(t theme.Theme, s deploy.Status) string {
	switch s {
	case deploy.Running:
		return t.Warn.Render("◐ running")
	case deploy.Done:
		return t.OK.Render("● ok     ")
	case deploy.Warned:
		return t.Warn.Render("◐ warning")
	case deploy.Failed:
		return t.Bad.Render("○ failed ")
	case deploy.Skipped:
		return t.Muted.Render("– skipped")
	}
	return t.Muted.Render("· waiting")
}

func outcomeLine(t theme.Theme, h *hostState) string {
	switch h.outcome {
	case deploy.Deployed:
		return t.OK.Render("● " + h.Alias + " " + string(h.outcome))
	case deploy.NotYetSynced, deploy.Unchanged:
		return t.Warn.Render("◐ " + h.Alias + " " + string(h.outcome))
	case deploy.RolledBack, deploy.RollbackFailed, deploy.DeployedAlert:
		return t.Bad.Render("○ " + h.Alias + " " + string(h.outcome))
	case "", deploy.NotStarted:
		if h.current != "" {
			return t.Warn.Render("◐ " + h.Alias + " " + string(h.current))
		}
		return t.Muted.Render("· " + h.Alias + " waiting")
	}
	return h.Alias + " " + string(h.outcome)
}

func (m *Model) progressView(t theme.Theme, ctx ui.Context) []string {
	var tabs []string
	for i, h := range m.hosts {
		s := outcomeLine(t, h)
		if i == m.selected%len(m.hosts) {
			s = "[" + s + "]"
		}
		tabs = append(tabs, s)
	}
	out := []string{strings.Join(tabs, "  "), ""}
	h := m.host()
	if h == nil {
		return out
	}
	for _, st := range deploy.Steps {
		s, ok := h.steps[st]
		if !ok && (st == deploy.StepKeys || st == deploy.StepRollback) {
			continue // shown only when they happen
		}
		line := fmt.Sprintf("  %s %-9s %s", symbol(t, s.status), st, s.detail)
		if st == deploy.StepSync && h.sync != nil && s.status == deploy.Running {
			line = fmt.Sprintf("  %s %-9s %s · ledger %d · age %ds", symbol(t, s.status), st, h.sync.State, h.sync.Ledger, h.sync.Age)
		}
		out = append(out, line)
	}
	if len(m.notes) > 0 {
		out = append(out, "")
		for _, n := range m.notes {
			out = append(out, t.Warn.Render("◐ "+n))
		}
	}
	if m.phase == phaseDone && m.err != nil {
		out = append(out, "", t.Bad.Render("○ "+firstLine(m.err.Error())))
	}
	return out
}

func (m *Model) status() string {
	if m.phase == phaseRunning {
		return "Deploying " + m.mode() + ". Polling for these hosts pauses until it ends."
	}
	n := 0
	for _, h := range m.hosts {
		if h.outcome == deploy.Deployed {
			n++
		}
	}
	return fmt.Sprintf("Finished: %d of %d deployed. Every step is in the audit log.", n, len(m.hosts))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
