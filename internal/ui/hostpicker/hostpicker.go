// Package hostpicker is the modal for adding hosts: a filterable,
// multi-select list of the concrete aliases in ~/.ssh/config (SPEC.md
// "Adding hosts").
package hostpicker

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
)

// Model is the host picker.
type Model struct {
	aliases  []string
	warning  string
	err      error
	loading  bool
	filter   string
	editing  bool // typing in the filter box
	cursor   int
	selected map[string]bool
	existing map[string]bool
}

// New returns a picker; aliases already in the inventory are shown but not
// selectable.
func New(existing []string) *Model {
	ex := map[string]bool{}
	for _, a := range existing {
		ex[a] = true
	}
	return &Model{selected: map[string]bool{}, existing: ex, loading: true}
}

// Init asks the root model to read ~/.ssh/config.
func (m *Model) Init() tea.Cmd { return ui.Emit(ui.LoadAliasesMsg{}) }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

// CapturingText implements ui.TextCapture.
func (m *Model) CapturingText() bool { return m.editing }

var (
	kMove   = key.NewBinding(key.WithKeys("up", "down", "j", "k"), key.WithHelp("↑↓", "move"))
	kToggle = key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select"))
	kAdd    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "add"))
	kFilter = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	kAll    = key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "select all shown"))
	kReload = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload"))
	kCancel = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	if m.editing {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "done")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
		}
	}
	return []key.Binding{kToggle, kAdd, kFilter, kReload, kCancel}
}

func (m *Model) visible() []string {
	if m.filter == "" {
		return m.aliases
	}
	var out []string
	f := strings.ToLower(m.filter)
	for _, a := range m.aliases {
		if strings.Contains(strings.ToLower(a), f) {
			out = append(out, a)
		}
	}
	return out
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case ui.AliasesMsg:
		m.loading = false
		m.aliases, m.warning, m.err = msg.Aliases, msg.Warning, msg.Err
		m.cursor = min(m.cursor, max(0, len(m.visible())-1))
		return m, nil
	case tea.KeyPressMsg:
		if m.editing {
			return m.updateFilter(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m *Model) updateFilter(msg tea.KeyPressMsg) (ui.Screen, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEnter:
		m.editing = false
	case msg.Code == tea.KeyEscape:
		m.editing, m.filter = false, ""
	case msg.Code == tea.KeyBackspace:
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case msg.Text != "":
		m.filter += msg.Text
	}
	m.cursor = min(m.cursor, max(0, len(m.visible())-1))
	return m, nil
}

func (m *Model) updateList(msg tea.KeyPressMsg) (ui.Screen, tea.Cmd) {
	vis := m.visible()
	switch msg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
	case "space":
		if m.cursor < len(vis) {
			a := vis[m.cursor]
			if !m.existing[a] {
				m.selected[a] = !m.selected[a]
			}
			if m.cursor < len(vis)-1 {
				m.cursor++
			}
		}
	case "ctrl+a":
		for _, a := range vis {
			if !m.existing[a] {
				m.selected[a] = true
			}
		}
	case "/":
		m.editing = true
	case "r":
		m.loading = true
		return m, ui.Emit(ui.LoadAliasesMsg{})
	case "esc":
		if m.filter != "" {
			m.filter = ""
			return m, nil
		}
		return m, ui.Emit(ui.CloseMsg{})
	case "enter":
		var chosen []string
		for a, ok := range m.selected {
			if ok {
				chosen = append(chosen, a)
			}
		}
		if len(chosen) == 0 && m.cursor < len(vis) && !m.existing[vis[m.cursor]] {
			chosen = []string{vis[m.cursor]}
		}
		if len(chosen) == 0 {
			return m, nil
		}
		// Keep ~/.ssh/config order.
		order := map[string]int{}
		for i, a := range m.aliases {
			order[a] = i
		}
		sort.Slice(chosen, func(i, j int) bool { return order[chosen[i]] < order[chosen[j]] })
		return m, tea.Batch(ui.Emit(ui.CloseMsg{}), ui.Emit(ui.AddHostsMsg{Aliases: chosen}))
	}
	return m, nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	var body []string
	filterLine := t.Muted.Render("/ to filter")
	if m.editing || m.filter != "" {
		filterLine = "Filter: " + m.filter
		if m.editing {
			filterLine += "▏"
		}
	}
	body = append(body, t.Header.Render("Add hosts from ~/.ssh/config"), filterLine)
	if m.warning != "" {
		body = append(body, t.Warn.Render("◐ "+m.warning))
	}
	body = append(body, "")
	switch {
	case m.loading:
		body = append(body, t.Muted.Render("Reading ~/.ssh/config…"))
	case m.err != nil:
		body = append(body, t.Bad.Render("○ "+m.err.Error()))
	case len(m.aliases) == 0:
		body = append(body, t.Muted.Render("No Host entries found in ~/.ssh/config. Add one there, then press r."))
	}

	vis := m.visible()
	listRows := max(1, ctx.Rows-len(body))
	start := 0
	if m.cursor >= listRows {
		start = m.cursor - listRows + 1
	}
	for i := start; i < len(vis) && i < start+listRows; i++ {
		a := vis[i]
		box := "[ ]"
		switch {
		case m.existing[a]:
			box = "[=]"
		case m.selected[a]:
			box = "[x]"
		}
		line := box + " " + a
		if m.existing[a] {
			line += t.Muted.Render("  already added")
		}
		line = components.Fit(line, ctx.Cols)
		if i == m.cursor {
			line = t.Text.Reverse(true).Render(line)
		}
		body = append(body, line)
	}

	n := 0
	for _, ok := range m.selected {
		if ok {
			n++
		}
	}
	status := fmt.Sprintf("%d aliases · %d selected", len(m.aliases), n)
	if m.filter != "" {
		status = fmt.Sprintf("%d of %d aliases match · %d selected", len(vis), len(m.aliases), n)
	}
	return ui.Layout{Subtitle: "add hosts", Body: body, Status: t.Muted.Render(status)}
}

// FullHelp implements ui.FullHelp.
func (m *Model) FullHelp() []key.Binding {
	long := func(b key.Binding, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
	}
	return []key.Binding{
		long(kMove, "Move"),
		long(kToggle, "Select or unselect the alias"),
		long(kAll, "Select every alias shown"),
		long(kAdd, "Add the selected aliases (or the one under the cursor)"),
		long(kFilter, "Filter the list"),
		long(kReload, "Read ~/.ssh/config again"),
		long(kCancel, "Clear the filter, or close without adding"),
	}
}
