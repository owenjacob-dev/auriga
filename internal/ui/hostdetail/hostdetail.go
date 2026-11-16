// Package hostdetail shows one host in tabs: Info, Quorum, Peers, Upgrades,
// Config and Probe (SPEC.md "Host detail view").
package hostdetail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/strkey"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/dashboard"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// Tab is one tab of the view.
type Tab int

const (
	TabInfo Tab = iota
	TabQuorum
	TabPeers
	TabUpgrades
	TabConfig
	TabProbe
	numTabs
)

var tabNames = [...]string{"Info", "Quorum", "Peers", "Upgrades", "Config", "Probe"}

type fetched struct {
	msg     ui.FetchedMsg
	loading bool
}

// Model is the host detail screen.
type Model struct {
	alias  string
	tab    Tab
	scroll [numTabs]int
	data   map[ui.AdminView]*fetched
	flash  string
	fleet  *ui.Fleet
	// showDiff switches the Config tab from the file to the deploy diff.
	showDiff bool
	version  string
}

// New opens alias on the Info tab. version is Auriga's, for renders.
func New(alias string) *Model {
	return &Model{alias: alias, data: map[ui.AdminView]*fetched{}}
}

// WithVersion sets the version shown in rendered configs.
func (m *Model) WithVersion(v string) *Model { m.version = v; return m }

// OpenOn opens alias on tab.
func OpenOn(alias string, tab Tab) *Model {
	m := New(alias)
	m.tab = tab
	return m
}

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// Init fetches what the opening tab needs.
func (m *Model) Init() tea.Cmd { return m.fetchFor(m.tab, false) }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

var (
	kTab        = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next"))
	kPrev       = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous tab"))
	kRefresh    = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh"))
	kCopy       = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy"))
	kTransitive = key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "transitive"))
	kScroll     = key.NewBinding(key.WithKeys("up", "down", "j", "k", "pgup", "pgdown"), key.WithHelp("↑↓", "scroll"))
	kBack       = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kDiff       = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff"))
	kRestore    = key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "restore a backup"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	keys := []key.Binding{kTab, kRefresh}
	if m.tab == TabQuorum {
		keys = append(keys, kTransitive)
	}
	if m.tab == TabConfig {
		keys = append(keys, kDiff)
		if h := m.host(); h != nil && len(h.Backups) > 0 {
			keys = append(keys, kRestore)
		}
	}
	if m.raw() != nil {
		keys = append(keys, kCopy)
	}
	return append(keys, kBack)
}

// FullHelp implements ui.FullHelp.
func (m *Model) FullHelp() []key.Binding {
	long := func(b key.Binding, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
	}
	return []key.Binding{
		long(kTab, "Next tab"), long(kPrev, "Previous tab"), long(kScroll, "Scroll"),
		long(kRefresh, "Fetch this tab again (and poll and probe the host)"),
		long(kTransitive, "Quorum tab: run the network-wide intersection check"),
		long(kCopy, "Copy this tab's raw JSON to the clipboard (OSC 52)"),
		long(kDiff, "Config tab: switch between the file and what a first deploy changes"),
		long(kRestore, "Config tab: compare a backup with the config and restore it"),
		long(kBack, "Back to the dashboard"),
	}
}

func (m *Model) viewFor(t Tab) (ui.AdminView, bool) {
	switch t {
	case TabQuorum:
		if f := m.data[ui.ViewQuorumTransitive]; f != nil {
			return ui.ViewQuorumTransitive, true
		}
		return ui.ViewQuorum, true
	case TabPeers:
		return ui.ViewPeers, true
	case TabUpgrades:
		return ui.ViewUpgrades, true
	}
	return 0, false
}

func (m *Model) fetchFor(t Tab, force bool) tea.Cmd {
	v, ok := m.viewFor(t)
	if !ok {
		return nil
	}
	if f := m.data[v]; f != nil && !force {
		return nil
	}
	return m.fetch(v)
}

func (m *Model) fetch(v ui.AdminView) tea.Cmd {
	f := m.data[v]
	if f == nil {
		f = &fetched{}
		m.data[v] = f
	}
	f.loading = true
	return ui.Emit(ui.FetchMsg{Alias: m.alias, View: v})
}

// raw is the JSON the current tab shows, for copy.
func (m *Model) raw() []byte {
	switch m.tab {
	case TabInfo:
		if h := m.fleet.Host(m.alias); h != nil {
			return h.InfoRaw
		}
	case TabQuorum, TabPeers, TabUpgrades:
		v, _ := m.viewFor(m.tab)
		if f := m.data[v]; f != nil {
			return f.msg.Raw
		}
	}
	return nil
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case ui.FetchedMsg:
		if msg.Alias != m.alias {
			return m, nil
		}
		m.data[msg.View] = &fetched{msg: msg}
		return m, nil
	case tea.KeyPressMsg:
		m.flash = ""
		switch msg.String() {
		case "esc":
			return m, ui.Emit(ui.CloseMsg{})
		case "tab":
			m.tab = (m.tab + 1) % numTabs
			return m, m.fetchFor(m.tab, false)
		case "shift+tab":
			m.tab = (m.tab + numTabs - 1) % numTabs
			return m, m.fetchFor(m.tab, false)
		case "r":
			if m.tab == TabQuorum {
				delete(m.data, ui.ViewQuorumTransitive)
			}
			return m, tea.Batch(ui.Emit(ui.RefreshHostMsg{Alias: m.alias}), m.fetchFor(m.tab, true))
		case "t":
			if m.tab == TabQuorum {
				return m, m.fetch(ui.ViewQuorumTransitive)
			}
		case "d":
			if m.tab == TabConfig {
				m.showDiff = !m.showDiff
				m.scroll[TabConfig] = 0
			}
		case "R":
			if h := m.host(); m.tab == TabConfig && h != nil && len(h.Backups) > 0 {
				return m, ui.Emit(ui.OpenRestoreMsg{Alias: m.alias})
			}
		case "c":
			if raw := m.raw(); raw != nil {
				m.flash = fmt.Sprintf("Copied %s JSON (%d bytes) to the clipboard.", strings.ToLower(tabNames[m.tab]), len(raw))
				return m, tea.SetClipboard(string(raw))
			}
		case "up", "k":
			m.scroll[m.tab] = max(0, m.scroll[m.tab]-1)
		case "down", "j":
			m.scroll[m.tab]++
		case "pgup":
			m.scroll[m.tab] = max(0, m.scroll[m.tab]-10)
		case "pgdown":
			m.scroll[m.tab] += 10
		}
	}
	return m, nil
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	if m.fleet == nil {
		m.fleet = ctx.Fleet
	}
	t := ctx.Theme
	h := ctx.Fleet.Host(m.alias)
	if h == nil {
		return ui.Layout{Subtitle: m.alias, Body: []string{t.Muted.Render("This host is no longer in the inventory.")}}
	}
	r := dashboard.Rows(ctx.Fleet, ctx.Now)[h.Alias]
	st := dashboard.Health(h, r)

	var tabs []string
	for i, name := range tabNames {
		if Tab(i) == m.tab {
			tabs = append(tabs, t.Header.Reverse(true).Render("["+name+"]"))
		} else {
			tabs = append(tabs, t.Muted.Render(" "+name+" "))
		}
	}
	head := []string{
		t.Style(st).Render(st.Symbol()+" "+st.Word()) + "  " + t.Header.Render(h.Alias) + "  " + strings.Join(tabs, ""),
		"",
	}

	var body []string
	switch m.tab {
	case TabInfo:
		body = m.infoTab(t, h, ctx)
	case TabQuorum:
		body = m.quorumTab(t, ctx)
	case TabPeers:
		body = m.peersTab(t, ctx)
	case TabUpgrades:
		body = m.upgradesTab(t, ctx)
	case TabConfig:
		body = m.configTab(t, h, ctx)
	case TabProbe:
		body = probeTab(t, h, ctx.Cols)
	}

	rows := ctx.Rows - len(head)
	if len(body) > rows {
		m.scroll[m.tab] = min(m.scroll[m.tab], len(body)-rows)
	} else {
		m.scroll[m.tab] = 0
	}
	body = body[m.scroll[m.tab]:]

	status := h.Alias + " · ssh " + h.Conn.State.String()
	if h.Transport != "" {
		status += " · " + h.Transport
	}
	if v, ok := m.viewFor(m.tab); ok {
		if f := m.data[v]; f != nil && !f.msg.At.IsZero() {
			status += " · fetched " + ago(ctx.Now.Sub(f.msg.At))
		}
	} else if m.tab == TabInfo && !h.InfoAt.IsZero() {
		status += " · last info " + ago(ctx.Now.Sub(h.InfoAt))
	}
	statusLine := t.Muted.Render(status)
	if m.flash != "" {
		statusLine = t.OK.Render(m.flash)
	}
	return ui.Layout{Subtitle: h.Alias, Body: append(head, body...), Status: statusLine}
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// highlighted are the info fields the dashboard columns come from.
var highlighted = []string{`"state"`, `"num"`, `"age"`, `"authenticated_count"`, `"pending_count"`, `"fail_at"`,
	`"intersection"`, `"protocol_version"`, `"version"`, `"build"`, `"agree"`, `"missing"`, `"Armed with`}

func (m *Model) infoTab(t theme.Theme, h *ui.HostView, ctx ui.Context) []string {
	var out []string
	if h.InfoErr != "" {
		out = append(out, wrap(t.Bad.Render("○ Last poll failed: ")+h.InfoErr, ctx.Cols)...)
		out = append(out, "")
	}
	if h.InfoRaw == nil {
		if h.Conn.State != sshx.StateConnected {
			return append(out, t.Muted.Render("Not connected; info is fetched once SSH connects."))
		}
		return append(out, t.Muted.Render("Waiting for the first info poll…"))
	}
	if i := h.Info; i != nil {
		label := func(k, v string) string { return t.Muted.Render(components.Fit(k, 10)) + v }
		failAt, inter := "—", "—"
		if q := i.Quorum.QSet; q != nil && q.FailAt != nil {
			failAt = fmt.Sprint(*q.FailAt)
		}
		if tr := i.Quorum.Transitive; tr != nil && tr.Intersection != nil {
			inter = map[bool]string{true: "yes", false: "NO"}[*tr.Intersection]
		}
		out = append(out,
			label("State", i.State)+"   "+label("Ledger", fmt.Sprintf("%d (age %ds)", i.Ledger.Num, i.Ledger.Age)),
			label("Peers", fmt.Sprintf("%d auth, %d pending", i.Peers.Authenticated, i.Peers.Pending))+"   "+
				label("Quorum", "fail_at "+failAt+", intersection "+inter),
			label("Protocol", fmt.Sprintf("supports %d, ledger at %d", i.ProtocolVersion, i.Ledger.Version))+"   "+label("Build", i.Build),
		)
		for _, st := range i.Status {
			out = append(out, label("Status", st))
		}
		out = append(out, "")
	}
	for _, line := range prettyJSON(h.InfoRaw) {
		hl := false
		for _, k := range highlighted {
			if strings.Contains(line, k) {
				hl = true
				break
			}
		}
		if hl {
			out = append(out, t.Header.Render(line))
		} else {
			out = append(out, line)
		}
	}
	return out
}

func prettyJSON(raw []byte) []string {
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return strings.Split(string(raw), "\n")
	}
	return strings.Split(b.String(), "\n")
}

func (m *Model) loadingOrErr(t theme.Theme, v ui.AdminView, ctx ui.Context) ([]string, bool) {
	f := m.data[v]
	switch {
	case f == nil || (f.loading && f.msg.Raw == nil && f.msg.Err == nil):
		what := "Fetching…"
		if v == ui.ViewQuorumTransitive {
			what = "Running the network-wide intersection check; this can take a while on pubnet…"
		}
		return []string{t.Muted.Render(what)}, false
	case f.msg.Err != nil:
		return wrap(t.Bad.Render("○ ")+f.msg.Err.Error(), ctx.Cols), false
	}
	return nil, true
}

// names maps public keys of inventory hosts to their aliases.
func (m *Model) names() map[string]string {
	n := map[string]string{}
	for _, h := range m.fleet.Hosts {
		if h.Config != nil && h.Config.PublicKey != "" {
			n[h.Config.PublicKey] = h.Alias
		}
	}
	return n
}

func (m *Model) nodeLabel(id string) string {
	if a, ok := m.names()[id]; ok {
		return a + " (" + strkey.Abbrev(id) + ")"
	}
	return strkey.Abbrev(id)
}

func (m *Model) quorumTab(t theme.Theme, ctx ui.Context) []string {
	v, _ := m.viewFor(TabQuorum)
	if out, ok := m.loadingOrErr(t, v, ctx); !ok {
		return out
	}
	q := m.data[v].msg.Quorum
	if q != nil && q.QSet == nil && q.Transitive != nil {
		return m.transitiveView(t, q)
	}
	if q == nil || q.QSet == nil {
		return []string{t.Muted.Render("The node returned no quorum information yet.")}
	}
	label := func(k, v string) string { return t.Muted.Render(components.Fit(k, 14)) + v }
	qs := q.QSet
	failAt := "—"
	if qs.FailAt != nil {
		failAt = fmt.Sprint(*qs.FailAt)
	}
	out := []string{
		label("Ledger", fmt.Sprintf("%d  phase %s", qs.Ledger, qs.Phase)),
		label("Agree", fmt.Sprint(qs.Agree.N())),
		label("fail_at", failAt+t.Muted.Render("  (nodes that can fail before this node loses quorum)")),
	}
	if len(qs.FailWith) > 0 {
		var names []string
		for _, id := range qs.FailWith {
			names = append(names, m.nodeLabel(id))
		}
		out = append(out, label("fail_with", strings.Join(names, ", ")))
	}
	for _, grp := range []struct {
		name  string
		nodes corehttp.CountOrList
		level theme.Status
	}{{"Missing", qs.Missing, theme.StatusDown}, {"Disagree", qs.Disagree, theme.StatusDown}, {"Delayed", qs.Delayed, theme.StatusDegraded}} {
		if grp.nodes.N() == 0 {
			out = append(out, label(grp.name, "none"))
			continue
		}
		out = append(out, label(grp.name, t.Style(grp.level).Render(fmt.Sprint(grp.nodes.N()))))
		for _, id := range grp.nodes.List {
			out = append(out, "    "+m.nodeLabel(id))
		}
	}
	out = append(out, "", t.Header.Render("Transitive"))
	out = append(out, m.transitiveLines(t, q.Transitive)...)
	if qs.Value != nil {
		out = append(out, "", t.Header.Render("Quorum set"))
		out = append(out, m.qsetLines(qs.Value, "  ")...)
	}
	return out
}

// transitiveView shows the result of quorum?transitive=true: the
// intersection summary and every node in the transitive quorum.
func (m *Model) transitiveView(t theme.Theme, q *corehttp.QuorumResponse) []string {
	out := []string{t.Header.Render("Transitive quorum (network-wide check)")}
	out = append(out, m.transitiveLines(t, q.Transitive)...)
	if len(q.Nodes) > 0 {
		out = append(out, "", t.Header.Render(components.Fit("NODE", 28)+components.Fit("DIST", 6)+components.Fit("STATUS", 12)+"HEARD"))
		nodes := append([]corehttp.TransitiveNode(nil), q.Nodes...)
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Distance < nodes[j].Distance })
		for _, n := range nodes {
			status := n.Status
			if status != "tracking" {
				status = t.Warn.Render(components.Fit(status, 12))
			} else {
				status = components.Fit(status, 12)
			}
			out = append(out, components.Fit(m.nodeLabel(n.Node), 28)+components.Fit(fmt.Sprint(n.Distance), 6)+status+fmt.Sprint(n.Heard))
		}
	}
	return append(out, "", t.Muted.Render("Press r to go back to this node's own quorum set."))
}

func (m *Model) transitiveLines(t theme.Theme, tr *corehttp.Transitive) []string {
	label := func(k, v string) string { return t.Muted.Render(components.Fit(k, 14)) + v }
	if tr == nil || tr.Intersection == nil {
		return []string{t.Muted.Render("No transitive summary; press t to run the check.")}
	}
	var out []string
	inter := t.OK.Render("● yes")
	if !*tr.Intersection {
		inter = t.Bad.Render("○ NO: the network can split")
	}
	out = append(out,
		label("Intersection", inter),
		label("Nodes", fmt.Sprintf("%d  (checked at ledger %d)", tr.NodeCount, tr.LastCheckLedger)),
	)
	if len(tr.Critical) > 0 {
		out = append(out, label("Critical", ""))
		for _, grp := range tr.Critical {
			var names []string
			for _, id := range grp {
				names = append(names, m.nodeLabel(id))
			}
			out = append(out, "    "+strings.Join(names, ", "))
		}
	}
	if len(tr.PotentialSplit) > 0 {
		out = append(out, label("Potential split", ""))
		for _, side := range tr.PotentialSplit {
			var names []string
			for _, id := range side {
				names = append(names, m.nodeLabel(id))
			}
			out = append(out, "    "+strings.Join(names, ", "))
		}
	}
	return out
}

func (m *Model) qsetLines(v *corehttp.QSetValue, indent string) []string {
	n := len(v.Nodes) + len(v.Inner)
	out := []string{fmt.Sprintf("%s%d of %d", indent, v.Threshold, n)}
	for _, id := range v.Nodes {
		out = append(out, indent+"  "+m.nodeLabel(id))
	}
	for _, inner := range v.Inner {
		out = append(out, m.qsetLines(inner, indent+"  ")...)
	}
	return out
}

func (m *Model) peersTab(t theme.Theme, ctx ui.Context) []string {
	if out, ok := m.loadingOrErr(t, ui.ViewPeers, ctx); !ok {
		return out
	}
	p := m.data[ui.ViewPeers].msg.Peers
	if p == nil {
		return []string{t.Muted.Render("No peers.")}
	}
	widths := []int{4, 22, 22, 9, 8, 8}
	row := func(cells ...string) string {
		var s string
		for i, c := range cells {
			s += components.Fit(c, widths[i])
		}
		return s
	}
	out := []string{t.Header.Render(row("DIR", "NODE", "ADDRESS", "LATENCY", "VERSION", "UP"))}
	add := func(dir string, peers []corehttp.Peer, pending bool) {
		sort.Slice(peers, func(i, j int) bool { return peers[i].Latency < peers[j].Latency })
		for _, pr := range peers {
			lat, up, node, ver := fmt.Sprintf("%dms", pr.Latency), fmt.Sprintf("%dm", pr.Elapsed/60), m.nodeLabel(pr.ID), ui.CoreVersion(pr.Version)
			if pending {
				lat, up = "pending", "—"
			}
			if pr.ID == "" {
				node = "—"
			}
			if ver == "" {
				ver = "—"
			}
			out = append(out, row(dir, node, pr.Address, lat, ver, up))
		}
	}
	add("in", p.Authenticated.Inbound, false)
	add("out", p.Authenticated.Outbound, false)
	add("in", p.Pending.Inbound, true)
	add("out", p.Pending.Outbound, true)
	total := len(p.Authenticated.Inbound) + len(p.Authenticated.Outbound)
	out = append(out, "", t.Muted.Render(fmt.Sprintf("%d authenticated (%d in, %d out), %d pending", total,
		len(p.Authenticated.Inbound), len(p.Authenticated.Outbound), len(p.Pending.Inbound)+len(p.Pending.Outbound))))
	return out
}

func (m *Model) upgradesTab(t theme.Theme, ctx ui.Context) []string {
	if out, ok := m.loadingOrErr(t, ui.ViewUpgrades, ctx); !ok {
		return out
	}
	f := m.data[ui.ViewUpgrades].msg
	h := ctx.Fleet.Host(m.alias)
	var out []string
	if h != nil && h.Info != nil {
		if a := h.Info.Armed(); a != "" {
			out = append(out, t.OK.Render("▲ "+a), "")
		} else {
			out = append(out, t.Muted.Render("info reports no armed upgrades."), "")
		}
	}
	out = append(out, t.Header.Render("upgrades?mode=get"))
	set := f.Upgrades.Scheduled()
	if len(set) == 0 {
		return append(out, t.Muted.Render("Nothing scheduled."))
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, t.Muted.Render(components.Fit(k, 26))+set[k])
	}
	return out
}

// configTab shows the classification, validation issues, backups and the
// file itself, or (d) the diff a first deploy would make.
func (m *Model) configTab(t theme.Theme, h *ui.HostView, ctx ui.Context) []string {
	if h.Probe == nil {
		return []string{t.Muted.Render("The config is read when the host is probed.")}
	}
	c := h.Class
	if c == nil {
		return []string{t.Bad.Render("○ The config could not be read: ") + h.ProbeErr}
	}
	var out []string
	switch c.Class {
	case stellarcfg.Unconfigured:
		return []string{t.Warn.Render("◐ Unconfigured: there is no config at this host's config path yet."),
			t.Muted.Render("The setup wizard (s on the dashboard) creates one.")}
	case stellarcfg.ReadOnly:
		out = append(out, t.Warn.Render("◐ Read-only: Auriga monitors this host and can arm upgrades, but will not rewrite its config."))
		for _, r := range c.Reasons {
			out = append(out, wrap("  · "+r, ctx.Cols)...)
		}
	case stellarcfg.Adoptable:
		out = append(out, t.OK.Render("● Adoptable: Auriga can manage it. Nothing changes until you deploy."))
	}

	if len(h.Issues) > 0 {
		out = append(out, "", t.Header.Render("Issues"))
		for _, i := range h.Issues {
			if i.Severity == stellarcfg.Block {
				out = append(out, wrap(t.Bad.Render("○ ")+i.Key+": "+i.Message, ctx.Cols)...)
			} else {
				out = append(out, wrap(t.Warn.Render("◐ ")+i.Key+": "+i.Message, ctx.Cols)...)
			}
		}
	}

	out = append(out, "", t.Header.Render("Backups"))
	if len(h.Backups) == 0 {
		out = append(out, t.Muted.Render("None yet. Each deploy keeps the ten newest."))
	}
	for _, b := range h.Backups {
		out = append(out, fmt.Sprintf("  %s  %s %s  %d bytes", b.Time.UTC().Format("2006-01-02 15:04:05Z"), b.Owner, b.Mode, b.Size))
	}

	if m.showDiff && c.Result != nil {
		out = append(out, "", t.Header.Render("What a first deploy changes"))
		for _, n := range stellarcfg.AdoptionNotes(c.Result) {
			out = append(out, wrap("  · "+n, ctx.Cols)...)
		}
		rendered, err := stellarcfg.Render(c.Result.Config, stellarcfg.RenderOptions{Version: m.version, Time: h.ProbedAt})
		if err != nil {
			return append(out, t.Bad.Render("○ "+err.Error()))
		}
		out = append(out, "")
		for _, l := range strings.Split(strings.TrimRight(stellarcfg.Diff("on the host", "rendered by auriga", h.Config.Text, rendered), "\n"), "\n") {
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

	out = append(out, "", t.Header.Render("On the host")+t.Muted.Render("  (NODE_SEED redacted there; the seed never left it)"))
	return append(out, strings.Split(strings.TrimRight(string(h.Config.Text), "\n"), "\n")...)
}

func probeTab(t theme.Theme, h *ui.HostView, cols int) []string {
	label := func(k, v string) string { return t.Muted.Render(components.Fit(k, 18)) + v }
	var lines []string
	for _, w := range dashboard.Warnings(h) {
		lines = append(lines, t.Warn.Render("◐ "+w))
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, t.Header.Render("Connection"), label("SSH", h.Conn.State.String()))
	if h.Conn.Address != "" {
		lines = append(lines, label("Address", h.Conn.Address))
	}
	if len(h.Conn.Route) > 1 {
		lines = append(lines, label("Route", strings.Join(h.Conn.Route, " → ")))
	}
	if h.Transport != "" {
		lines = append(lines, label("Admin endpoint", h.Transport))
	}
	if h.Conn.Err != "" && h.Conn.State != sshx.StateConnected {
		lines = append(lines, wrap(t.Bad.Render("Error: ")+h.Conn.Err, cols)...)
	}
	if h.Conn.Waiting {
		lines = append(lines, t.Muted.Render("Press r to retry once the problem is fixed."))
	}

	lines = append(lines, "", t.Header.Render("Probe"))
	switch {
	case h.Probing:
		lines = append(lines, t.Muted.Render("Probing…"))
	case h.ProbeErr != "":
		lines = append(lines, wrap(t.Bad.Render("Failed: ")+h.ProbeErr, cols)...)
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
	return lines
}

// wrap breaks text into lines of at most width cells.
func wrap(text string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len(line)+len(w)+1 > width-2 {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func (m *Model) host() *ui.HostView {
	if m.fleet == nil {
		return nil
	}
	return m.fleet.Host(m.alias)
}
