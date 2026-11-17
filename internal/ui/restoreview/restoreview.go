// Package restoreview is the manual rollback screen (SPEC.md "Manual
// rollback"): pick one of a host's backups, see how it differs from the
// config on the host, then hand it to the deploy screen, which runs steps
// 4 to 10 with that file.
package restoreview

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// Messages to and from the root model.
type (
	// ReadBackupMsg asks for a backup, read with its seed redacted on the host.
	ReadBackupMsg struct{ Alias, Path string }
	// BackupReadMsg is the answer.
	BackupReadMsg struct {
		Alias, Path string
		Config      remote.RedactedConfig
		Err         error
	}
	// RestoreMsg asks the root model to open the deploy screen for the backup.
	RestoreMsg struct {
		Alias, Path string
		Backup      remote.RedactedConfig
		Changes     int
	}
)

type phase int

const (
	phaseList phase = iota
	phaseLoading
	phaseDiff
)

// Model is the screen.
type Model struct {
	alias  string
	fleet  *ui.Fleet
	phase  phase
	cursor int
	backup remote.RedactedConfig
	path   string
	diff   []string
	err    string
	scroll int
}

// New opens the backups of alias.
func New(alias string) *Model { return &Model{alias: alias} }

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// Init implements ui.Screen.
func (m *Model) Init() tea.Cmd { return nil }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

var (
	kOpen    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "compare"))
	kRestore = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "restore this backup"))
	kMove    = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move"))
	kBack    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	switch m.phase {
	case phaseDiff:
		return []key.Binding{kRestore, kMove, kBack}
	case phaseLoading:
		return []key.Binding{kBack}
	}
	return []key.Binding{kOpen, kMove, kBack}
}

func (m *Model) host() *ui.HostView {
	if m.fleet == nil {
		return nil
	}
	return m.fleet.Host(m.alias)
}

func (m *Model) backups() []remote.Backup {
	if h := m.host(); h != nil {
		return h.Backups
	}
	return nil
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case BackupReadMsg:
		if msg.Alias != m.alias || msg.Path != m.path || m.phase != phaseLoading {
			return m, nil
		}
		if msg.Err != nil {
			m.phase, m.err = phaseList, msg.Err.Error()
			return m, nil
		}
		m.backup, m.phase, m.scroll = msg.Config, phaseDiff, 0
		var current []byte
		if h := m.host(); h != nil && h.Config != nil {
			current = h.Config.Text
		}
		m.diff = strings.Split(strings.TrimRight(stellarcfg.Diff("on the host now", "after the restore", current, msg.Config.Text), "\n"), "\n")
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Model) key(k string) tea.Cmd {
	list := m.backups()
	switch m.phase {
	case phaseList:
		switch k {
		case "esc":
			return ui.Emit(ui.CloseMsg{})
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(list)-1, m.cursor+1)
		case "enter":
			if m.cursor < len(list) {
				m.path, m.phase, m.err = list[m.cursor].Path, phaseLoading, ""
				return ui.Emit(ReadBackupMsg{Alias: m.alias, Path: m.path})
			}
		}
	case phaseLoading:
		if k == "esc" {
			m.phase = phaseList
		}
	case phaseDiff:
		switch k {
		case "esc":
			m.phase = phaseList
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		case "down", "j":
			m.scroll++
		case "pgdown":
			m.scroll += 10
		case "pgup":
			m.scroll = max(0, m.scroll-10)
		case "enter":
			n := 0
			for _, l := range m.diff {
				if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
					n++
				}
			}
			return ui.Emit(RestoreMsg{Alias: m.alias, Path: m.path, Backup: m.backup, Changes: n})
		}
	}
	return nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	l := ui.Layout{Subtitle: "restore a backup · " + m.alias}
	var body []string
	switch m.phase {
	case phaseList, phaseLoading:
		body = m.listView(t)
		l.Status = "Pick a backup to compare with the config on the host. Nothing changes yet."
		if m.phase == phaseLoading {
			l.Status = "Reading " + m.path + " with its seed redacted on the host…"
		}
	case phaseDiff:
		body = m.diffView(t)
		l.Status = "⏎ opens the deploy: it backs up the current config first, and rolls back if the node does not come back."
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

func (m *Model) listView(t theme.Theme) []string {
	list := m.backups()
	out := []string{t.Header.Render("Backups of " + m.alias + "'s config, newest first"), ""}
	if len(list) == 0 {
		return append(out, t.Muted.Render("None yet. Each deploy keeps the ten newest."))
	}
	for i, b := range list {
		line := fmt.Sprintf("%s  %s %s  %5d bytes  %s", b.Time.UTC().Format("2006-01-02 15:04:05Z"), b.Owner, b.Mode, b.Size, b.Path)
		if i == m.cursor {
			line = t.Header.Reverse(true).Render("> " + line)
		} else {
			line = "  " + line
		}
		out = append(out, line)
	}
	if m.err != "" {
		out = append(out, "", t.Bad.Render("○ "+m.err))
	}
	return out
}

func (m *Model) diffView(t theme.Theme) []string {
	out := []string{t.Header.Render("Restore " + m.path), ""}
	if m.backup.PublicKey != "" {
		key := "Node key in the backup: " + m.backup.PublicKey
		if h := m.host(); h != nil && h.Config != nil && h.Config.PublicKey != "" && h.Config.PublicKey != m.backup.PublicKey {
			key = t.Warn.Render("◐ The backup has another node key: " + m.backup.PublicKey + " (now " + h.Config.PublicKey + ")")
		}
		out = append(out, key, "")
	}
	lines := m.diff
	if len(lines) == 1 && lines[0] == "" {
		return append(out, t.Muted.Render("The backup is the same as the config on the host."))
	}
	if m.scroll < len(lines) {
		lines = lines[m.scroll:]
	}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
			out = append(out, t.OK.Render(l))
		case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
			out = append(out, t.Bad.Render(l))
		default:
			out = append(out, l)
		}
	}
	return out
}
