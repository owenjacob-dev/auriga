// Package hostdetail shows one host. In M1 it shows the connection and the
// probe results; M2 adds the Info, Quorum, Peers and Upgrades tabs, and this
// view becomes the Probe tab.
package hostdetail

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/dashboard"
)

// Model is the host detail screen.
type Model struct {
	alias  string
	scroll int
}

// New opens alias.
func New(alias string) *Model { return &Model{alias: alias} }

// Init implements ui.Screen.
func (m *Model) Init() tea.Cmd { return nil }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

var (
	kRefresh = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh"))
	kBack    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kScroll  = key.NewBinding(key.WithKeys("up", "down", "j", "k"), key.WithHelp("↑↓", "scroll"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding { return []key.Binding{kRefresh, kScroll, kBack} }

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc":
		return m, ui.Emit(ui.CloseMsg{})
	case "r":
		return m, ui.Emit(ui.RefreshHostMsg{Alias: m.alias})
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "down", "j":
		m.scroll++
	}
	return m, nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	h := ctx.Fleet.Host(m.alias)
	if h == nil {
		return ui.Layout{Subtitle: m.alias, Body: []string{t.Muted.Render("This host is no longer in the inventory.")}}
	}
	label := func(k, v string) string { return t.Muted.Render(components.Fit(k, 18)) + v }
	st := dashboard.Health(h, dashboard.Rows(ctx.Fleet, ctx.Now)[h.Alias])
	lines := []string{t.Style(st).Render(st.Symbol()+" "+st.Word()) + "  " + t.Header.Render(h.Alias)}
	// Problems first, so they are visible without scrolling.
	for _, w := range dashboard.Warnings(h) {
		lines = append(lines, t.Warn.Render("◐ "+w))
	}
	lines = append(lines,
		"",
		t.Header.Render("Connection"),
		label("SSH", h.Conn.State.String()),
	)
	if h.Conn.Address != "" {
		lines = append(lines, label("Address", h.Conn.Address))
	}
	if len(h.Conn.Route) > 1 {
		lines = append(lines, label("Route", strings.Join(h.Conn.Route, " → ")))
	}
	if h.Conn.Err != "" && h.Conn.State != sshx.StateConnected {
		lines = append(lines, wrapLabel(t.Bad.Render("Error"), h.Conn.Err, ctx.Cols)...)
	}
	if h.Conn.Waiting {
		lines = append(lines, t.Muted.Render("Press r to retry once the problem is fixed."))
	}

	lines = append(lines, "", t.Header.Render("Probe"))
	switch {
	case h.Probing:
		lines = append(lines, t.Muted.Render("Probing…"))
	case h.ProbeErr != "":
		lines = append(lines, wrapLabel(t.Bad.Render("Failed"), h.ProbeErr, ctx.Cols)...)
	case h.Probe == nil:
		lines = append(lines, t.Muted.Render("Not probed yet; the probe runs once SSH connects."))
	}
	if p := h.Probe; p != nil {
		yn := func(b bool) string {
			if b {
				return "yes"
			}
			return "no"
		}
		core := "not installed"
		if p.CoreInstalled {
			core = p.CoreVersion
		}
		lines = append(lines,
			label("Host", strings.TrimSpace(p.Hostname+"  "+p.OS)),
			label("stellar-core", core),
			label("Service", fmt.Sprintf("%s, %s (unit %s)", p.ServiceActive, p.ServiceEnabled, p.ServiceLoad)),
			label("stellar user", yn(p.StellarUser)),
		)
		if p.ConfigPresent {
			lines = append(lines, label("Config", fmt.Sprintf("present, %s, mode %s", p.ConfigOwner, p.ConfigMode)))
		} else {
			lines = append(lines, label("Config", "not found"))
		}
		if p.HTTPPort != 0 {
			lines = append(lines, label("HTTP_PORT", fmt.Sprint(p.HTTPPort)))
		}
		lines = append(lines,
			label("Bucket dir", p.BucketDir),
			label("Disk free", fmt.Sprintf("%.1f GB of %.1f GB", float64(p.DiskAvailKB)/(1<<20), float64(p.DiskTotalKB)/(1<<20))),
			label("NTP synchronised", p.NTPSync),
		)
	}
	if c := h.Config; c != nil {
		key := "could not be derived"
		switch {
		case c.Seed == "absent":
			key = "no NODE_SEED in config"
		case c.PublicKey != "":
			key = c.PublicKey
		}
		lines = append(lines, label("Node public key", key))
		if c.NodeAlias != "" {
			lines = append(lines, label("Node alias", c.NodeAlias))
		}
		if h.HomeDomain != "" {
			lines = append(lines, label("Home domain", h.HomeDomain))
		}
		if h.Network != "" {
			lines = append(lines, label("Network", h.Network))
		}
	}

	if len(lines) > ctx.Rows {
		m.scroll = min(m.scroll, len(lines)-ctx.Rows)
	} else {
		m.scroll = 0
	}
	return ui.Layout{
		Subtitle: h.Alias,
		Body:     lines[m.scroll:],
		Status:   t.Muted.Render(fmt.Sprintf("%s · ssh %s", h.Alias, h.Conn.State)),
	}
}

// wrapLabel renders a long value under a label, wrapped to width.
func wrapLabel(label, text string, width int) []string {
	out := []string{components.Fit(label, 18) + ""}
	words := strings.Fields(text)
	line := ""
	for _, w := range words {
		if len(line)+len(w)+1 > width-2 && line != "" {
			out = append(out, "  "+line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, "  "+line)
	}
	return out
}
