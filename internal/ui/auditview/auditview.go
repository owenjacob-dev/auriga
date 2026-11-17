// Package auditview is the audit log screen (SPEC.md "Screens" 7): every
// change Auriga made to a host, newest first, filterable by host or action.
package auditview

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
)

type (
	// LoadMsg asks the root model for the log.
	LoadMsg struct{}
	// LoadedMsg carries it, newest first.
	LoadedMsg struct {
		Entries []audit.Entry
		Bad     int // lines that did not parse
		Path    string
		Err     error
	}
)

// Model is the screen.
type Model struct {
	entries   []audit.Entry
	bad       int
	path      string
	err       error
	loaded    bool
	filter    string
	filtering bool
	scroll    int
}

// New opens the log.
func New() *Model { return &Model{} }

// Init asks for the log.
func (m *Model) Init() tea.Cmd { return ui.Emit(LoadMsg{}) }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

// CapturingText implements ui.TextCapture while the filter is typed.
func (m *Model) CapturingText() bool { return m.filtering }

var (
	kFilter  = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	kRefresh = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload"))
	kScroll  = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "scroll"))
	kBack    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kDone    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "done"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	if m.filtering {
		return []key.Binding{kDone, kBack}
	}
	return []key.Binding{kFilter, kRefresh, kScroll, kBack}
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case LoadedMsg:
		m.entries, m.bad, m.path, m.err, m.loaded = msg.Entries, msg.Bad, msg.Path, msg.Err, true
	case tea.KeyPressMsg:
		k := msg.String()
		if m.filtering {
			switch {
			case k == "enter":
				m.filtering = false
			case k == "esc":
				m.filtering, m.filter = false, ""
			case k == "backspace" && m.filter != "":
				_, n := utf8.DecodeLastRuneInString(m.filter)
				m.filter = m.filter[:len(m.filter)-n]
			case msg.Text != "":
				m.filter += msg.Text
			}
			m.scroll = 0
			return m, nil
		}
		switch k {
		case "esc":
			if m.filter != "" {
				m.filter = ""
				return m, nil
			}
			return m, ui.Emit(ui.CloseMsg{})
		case "/":
			m.filtering = true
		case "r":
			return m, ui.Emit(LoadMsg{})
		case "down", "j":
			m.scroll++
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		case "pgdown":
			m.scroll += 10
		case "pgup":
			m.scroll = max(0, m.scroll-10)
		}
	}
	return m, nil
}

// visible applies the filter: a substring of the host or the action.
func (m *Model) visible() []audit.Entry {
	if m.filter == "" {
		return m.entries
	}
	var out []audit.Entry
	for _, e := range m.entries {
		if strings.Contains(e.Host, m.filter) || strings.Contains(e.Action, m.filter) {
			out = append(out, e)
		}
	}
	return out
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	l := ui.Layout{Subtitle: "audit log"}
	var body []string
	switch {
	case !m.loaded:
		body = []string{t.Muted.Render("Reading the audit log…")}
	case m.err != nil:
		body = []string{t.Bad.Render("○ The audit log could not be read: " + m.err.Error())}
	default:
		list := m.visible()
		head := fmt.Sprintf("%-19s  %-10s  %-15s  %-6s  %s", "TIME (UTC)", "HOST", "ACTION", "RESULT", "DETAIL")
		body = append(body, t.Header.Render(components.Fit(head, ctx.Cols)))
		if len(list) == 0 {
			msg := "Nothing yet: deploys and restores are logged here."
			if m.filter != "" {
				msg = "No entry matches " + m.filter + "."
			}
			body = append(body, t.Muted.Render(msg))
		}
		if m.scroll >= len(list) {
			m.scroll = max(0, len(list)-1)
		}
		for _, e := range list[m.scroll:] {
			result := t.OK.Render("● ok  ")
			if e.Result != audit.OK {
				result = t.Bad.Render("○ " + components.Fit(e.Result, 4))
			}
			line := fmt.Sprintf("%-19s  %-10s  %-15s  %s  %s", e.Time.UTC().Format("2006-01-02 15:04:05"),
				components.Fit(e.Host, 10), components.Fit(e.Action, 15), result, detail(e))
			body = append(body, components.Fit(line, ctx.Cols))
		}
	}
	if len(body) > ctx.Rows {
		body = body[:ctx.Rows]
	}
	l.Body = body
	switch {
	case m.filtering:
		l.Status = "Filter (host or action): " + m.filter + "▏"
	case m.filter != "":
		l.Status = fmt.Sprintf("%d of %d entries match %q · esc clears", len(m.visible()), len(m.entries), m.filter)
	default:
		l.Status = fmt.Sprintf("%d entries · %s", len(m.entries), m.path)
		if m.bad > 0 {
			l.Status += fmt.Sprintf(" · %d unreadable lines skipped", m.bad)
		}
	}
	return l
}

func detail(e audit.Entry) string {
	var parts []string
	if e.Error != "" {
		parts = append(parts, firstLine(e.Error))
	}
	if e.Backup != "" {
		parts = append(parts, "bak "+backupStamp(e.Backup))
	}
	if e.ConfigSHA256 != "" {
		parts = append(parts, "sha256 "+short(e.ConfigSHA256))
	}
	if e.PublicKey != "" {
		parts = append(parts, "key "+e.PublicKey[:min(8, len(e.PublicKey))]+"…")
	}
	if e.Detail != "" && e.Error == "" {
		parts = append(parts, firstLine(e.Detail))
	}
	return strings.Join(parts, " · ")
}

// backupStamp shortens "<config>.auriga-<UTC>.bak" to its timestamp.
func backupStamp(p string) string {
	if i := strings.LastIndex(p, ".auriga-"); i >= 0 && strings.HasSuffix(p, ".bak") {
		return strings.TrimSuffix(p[i+len(".auriga-"):], ".bak")
	}
	return p
}

func short(s string) string { return s[:min(12, len(s))] }

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
