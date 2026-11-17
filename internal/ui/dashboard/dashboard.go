// Package dashboard is the home screen: one row per host with live `info`
// from the admin endpoint, coloured by the rules in internal/health (SPEC.md
// "Dashboard").
package dashboard

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/owenjacob-dev/auriga/internal/health"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// LowDiskKB is the free-space warning threshold (SPEC.md "Probe": 20 GB).
const LowDiskKB = 20 * 1024 * 1024

type column struct {
	title string
	width int
	wide  bool // shown only when the terminal is wide enough
}

// columns follow SPEC.md's sketch; BUILD and UP SINCE appear on wide
// terminals (the Info tab always shows them).
var columns = []column{
	{"", 2, false}, {"HOST", 12, false}, {"STATE", 14, false}, {"LEDGER", 10, false},
	{"AGE", 5, false}, {"PEERS", 6, false}, {"QUORUM", 9, false}, {"INT", 4, false},
	{"PROTO", 6, false}, {"UPGRADE", 8, false}, {"BUILD", 10, true}, {"UP SINCE", 13, true},
}

func visibleColumns(width int) []int {
	total := 0
	var idx []int
	for i, c := range columns {
		if !c.wide {
			total += c.width
			idx = append(idx, i)
		}
	}
	for i, c := range columns {
		if c.wide && total+c.width <= width {
			total += c.width
			idx = append(idx, i)
		}
	}
	return idx
}

type keys struct {
	Up, Down, Open, Add, Refresh, Remove, Pause, Setup, Log, Upgrades key.Binding
}

func newKeys() keys {
	return keys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "Move up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "Move down")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		Add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Remove:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
		Pause:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
		Setup:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "setup")),
		Log:      key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "log")),
		Upgrades: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upgrades")),
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
	return []key.Binding{k.Add, k.Setup, k.Upgrades, k.Open, k.Refresh, k.Log}
}

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// wizardFor opens the wizard for the selected host's org, preselecting the
// org's hosts (or just the selected host when it has no org).
func (m *Model) wizardFor() ui.OpenWizardMsg {
	i := m.index()
	if i < 0 {
		return ui.OpenWizardMsg{}
	}
	sel := m.fleet.Hosts[i]
	if sel.Org == "" {
		return ui.OpenWizardMsg{Hosts: []string{sel.Alias}}
	}
	var hosts []string
	for _, h := range m.fleet.Hosts {
		if h.Org == sel.Org {
			hosts = append(hosts, h.Alias)
		}
	}
	return ui.OpenWizardMsg{Org: sel.Org, Hosts: hosts}
}

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
		long(k.Setup, "s", "Setup wizard for the selected host's org"),
		long(k.Refresh, "r", "Poll and probe now, or reconnect a disconnected host"),
		long(k.Pause, "p", "Pause or resume polling"),
		long(k.Upgrades, "u", "Network upgrade votes for the selected host's org"),
		long(k.Log, "l", "Audit log of every change Auriga made to a host"),
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
	case key.Matches(km, m.keys.Pause):
		return m, ui.Emit(ui.TogglePauseMsg{})
	case key.Matches(km, m.keys.Setup):
		return m, ui.Emit(m.wizardFor())
	case key.Matches(km, m.keys.Log):
		return m, ui.Emit(ui.OpenAuditMsg{})
	case key.Matches(km, m.keys.Upgrades):
		org := ""
		if i := m.index(); i >= 0 {
			org = m.fleet.Hosts[i].Org
		}
		return m, ui.Emit(ui.OpenUpgradesMsg{Org: org})
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
	cols := visibleColumns(ctx.Cols)
	var header string
	for _, i := range cols {
		header += components.Fit(columns[i].title, columns[i].width)
	}
	if ctx.Fleet == nil || len(ctx.Fleet.Hosts) == 0 {
		body := []string{t.Header.Render(header), "", t.Muted.Render("No hosts yet. Press a to add hosts from ~/.ssh/config.")}
		return ui.Layout{Body: body, Status: m.flash(ctx, t.Muted.Render("No hosts in the inventory"))}
	}
	rows := Rows(ctx.Fleet, ctx.Now)

	var body []string
	for _, h := range ctx.Fleet.Hosts {
		if rows[h.Alias].IntersectionLost {
			body = append(body, t.Banner.Render(components.Fit(
				"○ NO QUORUM INTERSECTION ("+h.Alias+"): network can split. Check quorum sets.", ctx.Cols)))
			break
		}
	}
	body = append(body, t.Header.Render(header))
	sel := m.index()
	for i, h := range ctx.Fleet.Hosts {
		body = append(body, m.row(t, h, rows[h.Alias], cols, i == sel))
	}
	if m.confirmRemove != "" {
		body = append(body, "", t.Warn.Render(fmt.Sprintf("Remove %s from the inventory? Nothing on the host changes.", m.confirmRemove)))
	}
	status := ""
	if sel >= 0 {
		h := ctx.Fleet.Hosts[sel]
		status = statusLine(t, h, rows[h.Alias], ctx.Now)
	}
	return ui.Layout{Subtitle: subtitle(ctx.Fleet), Body: body, Status: m.flash(ctx, status)}
}

// Rows evaluates the health of every host in the fleet.
func Rows(f *ui.Fleet, now time.Time) map[string]health.Row {
	samples := make([]health.Sample, 0, len(f.Hosts))
	for _, h := range f.Hosts {
		samples = append(samples, health.Sample{
			Alias: h.Alias, Org: h.Org, Network: h.Network,
			Info: h.Info, At: h.InfoAt, NotSyncedSince: h.NotSyncedSince,
		})
	}
	return health.Evaluate(samples, now, f.PollInterval)
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
	if f.Paused {
		s += " · polling paused"
	}
	return s
}

// Health returns a host's overall status: its connection, its polled
// health and its probe findings.
func Health(h *ui.HostView, r health.Row) theme.Status {
	switch h.Conn.State {
	case sshx.StateConnected:
	case sshx.StateConnecting, sshx.StateReconnecting:
		return theme.StatusDegraded
	default:
		return theme.StatusDown
	}
	switch {
	case r.Overall == health.Bad, h.PollFailures >= health.FailuresBeforeSlow, len(Dangers(h)) > 0:
		return theme.StatusDown
	case h.Info == nil, h.Probing, h.ProbeErr != "", h.InfoErr != "", r.Overall == health.Warn, len(Warnings(h)) > 0:
		return theme.StatusDegraded
	}
	return theme.StatusOK
}

// Dangers lists findings that put the node at risk; they turn the host red.
func Dangers(h *ui.HostView) []string {
	var d []string
	if h.Class != nil && h.Class.Result != nil {
		if c := h.Class.Result.Config; c.PublicHTTPPort != nil && *c.PublicHTTPPort {
			d = append(d, "PUBLIC_HTTP_PORT=true: the admin endpoint is open to the network")
		}
	}
	return d
}

// Warnings lists probe and config findings that need attention.
func Warnings(h *ui.HostView) []string {
	var w []string
	if c := h.Class; c != nil {
		switch c.Class {
		case stellarcfg.ReadOnly:
			w = append(w, "config is read-only: "+c.Reasons[0])
		case stellarcfg.Unconfigured:
			w = append(w, "no config yet: run the setup wizard")
		}
	}
	nBlock, nWarn := 0, 0
	for _, i := range h.Issues {
		if i.Rule == "public-http-port" {
			continue // reported as a danger
		}
		if i.Severity == stellarcfg.Block {
			nBlock++
		} else {
			nWarn++
		}
	}
	switch {
	case nBlock > 0:
		w = append(w, fmt.Sprintf("%d config problems to fix before a deploy", nBlock))
	case nWarn > 0:
		w = append(w, fmt.Sprintf("%d config warnings", nWarn))
	}
	p := h.Probe
	if p == nil {
		return w
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

func (m *Model) row(t theme.Theme, h *ui.HostView, r health.Row, cols []int, selected bool) string {
	st := Health(h, r)
	state := r.State
	if h.Conn.State != sshx.StateConnected {
		state = health.Cell{Text: h.Conn.State.String(), Level: health.Bad}
		if h.Conn.State == sshx.StateConnecting || h.Conn.State == sshx.StateReconnecting {
			state.Level = health.Warn
		}
	} else if h.Info == nil {
		state = health.Cell{Text: "polling…", Level: health.Unknown}
		if h.InfoErr != "" {
			state = health.Cell{Text: "no info", Level: health.Bad}
		}
	}
	cells := []health.Cell{
		{Text: st.Symbol()}, {Text: h.Alias}, state, r.Ledger, r.Age, r.Peers, r.Quorum,
		r.Intersection, r.Protocol, r.Upgrade, r.Build, r.UpSince,
	}
	var out string
	for _, i := range cols {
		c := cells[i]
		text := components.Fit(c.Text, columns[i].width)
		if selected {
			out += text
			continue
		}
		switch i {
		case 0:
			out += t.Style(st).Render(text)
		case 1:
			out += text
		default:
			out += cellStyle(t, c).Render(text)
		}
	}
	if selected {
		return t.Style(st).Reverse(true).Render(out)
	}
	return out
}

func cellStyle(t theme.Theme, c health.Cell) lipgloss.Style {
	if c.Stale {
		return t.Stale
	}
	switch c.Level {
	case health.OK:
		return t.OK
	case health.Warn:
		return t.Warn
	case health.Bad:
		return t.Bad
	}
	return t.Muted
}

func statusLine(t theme.Theme, h *ui.HostView, r health.Row, now time.Time) string {
	// What needs attention comes before long error text, which may be cut
	// off at the frame edge.
	s := h.Alias + " · ssh " + h.Conn.State.String()
	if h.Conn.Waiting {
		s += " · press r to retry"
	}
	if h.Conn.State != sshx.StateConnected {
		if !h.Conn.NextRetry.IsZero() && h.Conn.NextRetry.After(now) {
			s += fmt.Sprintf(" · retry in %ds", int(h.Conn.NextRetry.Sub(now).Seconds()+0.5))
		}
		if h.Conn.Err != "" {
			s += " · " + h.Conn.Err
		}
		return t.Bad.Render(s)
	}
	if h.Transport != "" {
		s += " · " + h.Transport
	}
	problems := append([]string(nil), r.Problems...)
	if h.InfoErr != "" {
		problems = append([]string{"poll failed: " + h.InfoErr}, problems...)
	}
	if h.ProbeErr != "" {
		problems = append(problems, "probe failed: "+h.ProbeErr)
	}
	problems = append(append(Dangers(h), problems...), Warnings(h)...)
	if !h.InfoAt.IsZero() {
		s += " · last info " + ago(now.Sub(h.InfoAt))
	}
	if len(problems) > 0 {
		style := t.Warn
		if r.Overall == health.Bad || h.InfoErr != "" || len(Dangers(h)) > 0 {
			style = t.Bad
		}
		return style.Render(h.Alias + " · " + strings.Join(problems, " · ") + " · " + strings.TrimPrefix(s, h.Alias+" · "))
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
