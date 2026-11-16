// Package dashboard is the home screen: one row per host.
//
// In M1 a row shows the SSH connection and the probe summary. M2 replaces
// the probe columns with the live `info` columns from SPEC.md "Dashboard"
// and moves the probe summary to the host detail view.
package dashboard

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/strkey"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// LowDiskKB is the free-space warning threshold (SPEC.md "Probe": 20 GB).
const LowDiskKB = 20 * 1024 * 1024

type column struct {
	title string
	width int
}

var columns = []column{
	{"", 2}, {"HOST", 12}, {"SSH", 14}, {"CORE", 9}, {"SERVICE", 10},
	{"NODE KEY", 11}, {"FREE", 7}, {"NTP", 4},
}

type keys struct {
	Up, Down, Open, Add, Refresh, Remove key.Binding
}

func newKeys() keys {
	return keys{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "Move up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "Move down")),
		Open:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		Add:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Remove:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
	}
}

// Model is the dashboard screen.
type Model struct {
	keys     keys
	selected string // alias of the selected row
	// confirmRemove is the alias awaiting a y/n removal confirmation.
	confirmRemove string
	fleet         *ui.Fleet
}

// New returns the dashboard.
func New() *Model { return &Model{keys: newKeys()} }

// Init implements ui.Screen.
func (m *Model) Init() tea.Cmd { return nil }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	if m.confirmRemove != "" {
		return []key.Binding{
			key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "remove from inventory")),
			key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "keep")),
		}
	}
	k := m.keys
	return []key.Binding{k.Add, k.Open, k.Refresh, k.Remove}
}

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// FullHelp implements ui.FullHelp.
func (m *Model) FullHelp() []key.Binding {
	long := func(b key.Binding, keys, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(keys, desc))
	}
	k := m.keys
	return []key.Binding{
		long(k.Up, "↑ k", "Move up"),
		long(k.Down, "↓ j", "Move down"),
		long(k.Open, "enter", "Open the selected host"),
		long(k.Add, "a", "Add hosts from ~/.ssh/config"),
		long(k.Refresh, "r", "Probe again, or reconnect a disconnected host"),
		long(k.Remove, "x", "Remove the host from the inventory (local only)"),
	}
}

func (m *Model) index() int {
	if m.fleet == nil {
		return -1
	}
	for i, h := range m.fleet.Hosts {
		if h.Alias == m.selected {
			return i
		}
	}
	if len(m.fleet.Hosts) > 0 {
		m.selected = m.fleet.Hosts[0].Alias
		return 0
	}
	return -1
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if m.confirmRemove != "" {
		switch km.String() {
		case "y", "Y":
			alias := m.confirmRemove
			m.confirmRemove = ""
			return m, ui.Emit(ui.RemoveHostMsg{Alias: alias})
		case "n", "N", "esc":
			m.confirmRemove = ""
		}
		return m, nil
	}
	i := m.index()
	switch {
	case key.Matches(km, m.keys.Add):
		return m, ui.Emit(ui.OpenHostPickerMsg{})
	case i < 0:
		return m, nil
	case key.Matches(km, m.keys.Up):
		if i > 0 {
			m.selected = m.fleet.Hosts[i-1].Alias
		}
	case key.Matches(km, m.keys.Down):
		if i < len(m.fleet.Hosts)-1 {
			m.selected = m.fleet.Hosts[i+1].Alias
		}
	case key.Matches(km, m.keys.Open):
		return m, ui.Emit(ui.OpenHostMsg{Alias: m.selected})
	case key.Matches(km, m.keys.Refresh):
		return m, ui.Emit(ui.RefreshHostMsg{Alias: m.selected})
	case key.Matches(km, m.keys.Remove):
		m.confirmRemove = m.selected
	}
	return m, nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	if m.fleet == nil {
		m.fleet = ctx.Fleet
	}
	t := ctx.Theme
	var header string
	for _, c := range columns {
		header += components.Fit(c.title, c.width)
	}
	body := []string{t.Header.Render(header)}
	if ctx.Fleet == nil || len(ctx.Fleet.Hosts) == 0 {
		body = append(body, "", t.Muted.Render("No hosts yet. Press a to add hosts from ~/.ssh/config."))
		return ui.Layout{Body: body, Status: m.flash(ctx, t.Muted.Render("No hosts in the inventory"))}
	}
	sel := m.index()
	for i, h := range ctx.Fleet.Hosts {
		body = append(body, row(t, h, i == sel))
	}
	if m.confirmRemove != "" {
		body = append(body, "", t.Warn.Render(fmt.Sprintf("Remove %s from the inventory? Nothing on the host changes.", m.confirmRemove)))
	}
	status := ""
	if sel >= 0 {
		status = statusLine(t, ctx.Fleet.Hosts[sel], ctx.Now)
	}
	return ui.Layout{Subtitle: subtitle(ctx.Fleet), Body: body, Status: m.flash(ctx, status)}
}

func (m *Model) flash(ctx ui.Context, status string) string {
	if ctx.Fleet != nil && ctx.Fleet.Flash != "" {
		return ctx.Theme.Bad.Render(ctx.Fleet.Flash)
	}
	return status
}

func subtitle(f *ui.Fleet) string {
	orgs, nets := map[string]bool{}, map[string]bool{}
	for _, h := range f.Hosts {
		if h.Org != "" {
			orgs[h.Org] = true
		}
		if h.Network != "" {
			nets[h.Network] = true
		}
	}
	s := fmt.Sprintf("%d hosts", len(f.Hosts))
	if len(f.Hosts) == 1 {
		s = "1 host"
	}
	if len(orgs) == 1 {
		for o := range orgs {
			s = "org " + o
		}
	}
	if len(nets) == 1 {
		for n := range nets {
			s += " · " + n
		}
	}
	return s
}

// Health returns the row status for a host.
func Health(h *ui.HostView) theme.Status {
	switch h.Conn.State {
	case sshx.StateConnected:
	case sshx.StateConnecting, sshx.StateReconnecting:
		return theme.StatusDegraded
	default:
		return theme.StatusDown
	}
	if h.Probing || h.Probe == nil {
		return theme.StatusDegraded
	}
	if h.ProbeErr != "" || len(Warnings(h)) > 0 {
		return theme.StatusDegraded
	}
	return theme.StatusOK
}

// Warnings lists probe findings that need attention.
func Warnings(h *ui.HostView) []string {
	var w []string
	p := h.Probe
	if p == nil {
		return nil
	}
	if !p.CoreInstalled {
		w = append(w, "stellar-core is not installed")
	}
	if p.ConfigPresent && p.WorldReadable() {
		w = append(w, fmt.Sprintf("config is world-readable (mode %s)", p.ConfigMode))
	}
	if p.DiskAvailKB > 0 && p.DiskAvailKB < LowDiskKB {
		w = append(w, fmt.Sprintf("only %s free for buckets", humanKB(p.DiskAvailKB)))
	}
	switch p.NTPSync {
	case "yes":
	case "no":
		w = append(w, "clock is not NTP-synchronised")
	default:
		w = append(w, "clock sync unknown (no timedatectl)")
	}
	return w
}

func row(t theme.Theme, h *ui.HostView, selected bool) string {
	st := Health(h)
	core, svc, key, free, ntp := "—", "—", "—", "—", "—"
	if p := h.Probe; p != nil {
		if p.CoreInstalled {
			core = ui.CoreVersion(p.CoreVersion)
		} else {
			core = "missing"
		}
		svc = p.ServiceActive
		if p.ServiceLoad == "not-found" {
			svc = "no unit"
		}
		if p.DiskAvailKB > 0 {
			free = humanKB(p.DiskAvailKB)
		}
		ntp = p.NTPSync
		if ntp != "yes" && ntp != "no" {
			ntp = "?"
		}
	}
	if h.Config != nil && h.Config.PublicKey != "" {
		key = strkey.Abbrev(h.Config.PublicKey)
	} else if h.Config != nil && h.Config.Seed == "absent" {
		key = "no seed"
	}
	conn := h.Conn.State.String()
	if h.Probing && h.Conn.State == sshx.StateConnected {
		conn = "probing"
	}
	cells := []string{
		st.Symbol(), h.Alias, conn, core, svc, key, free, ntp,
	}
	var out string
	for i, c := range cells {
		out += components.Fit(c, columns[i].width)
	}
	style := t.Style(st)
	if selected {
		return style.Reverse(true).Render(out)
	}
	return style.Render(out[:len(st.Symbol())]) + out[len(st.Symbol()):]
}

func statusLine(t theme.Theme, h *ui.HostView, now time.Time) string {
	// The action the operator can take comes before long error text, which
	// may be cut off at the frame edge.
	s := h.Alias + " · ssh " + h.Conn.State.String()
	if h.Conn.Waiting {
		s += " · press r to retry"
	}
	if h.Conn.State != sshx.StateConnected && !h.Conn.NextRetry.IsZero() && h.Conn.NextRetry.After(now) {
		s += fmt.Sprintf(" · retry in %ds", int(h.Conn.NextRetry.Sub(now).Seconds()+0.5))
	}
	switch {
	case h.Conn.Err != "" && h.Conn.State != sshx.StateConnected:
		s += " · " + h.Conn.Err
	case h.ProbeErr != "":
		s += " · probe failed: " + h.ProbeErr
	case len(h.Conn.Route) > 1:
		s += " · via " + h.Conn.Route[len(h.Conn.Route)-2]
	}
	if !h.ProbedAt.IsZero() && h.ProbeErr == "" {
		s += " · probed " + ago(now.Sub(h.ProbedAt))
	}
	if w := Warnings(h); len(w) > 0 {
		return t.Warn.Render(s + " · " + w[0])
	}
	return t.Muted.Render(s)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func humanKB(kb int64) string {
	switch {
	case kb >= 1024*1024*1024:
		return fmt.Sprintf("%.1fT", float64(kb)/(1024*1024*1024))
	case kb >= 1024*1024:
		return fmt.Sprintf("%dG", kb/(1024*1024))
	default:
		return fmt.Sprintf("%dM", kb/1024)
	}
}
