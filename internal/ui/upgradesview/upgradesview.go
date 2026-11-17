// Package upgradesview is the upgrades screen (SPEC.md "Network
// upgrades"): an overview of every host's protocol, fees and armed vote;
// arming with a form or a pasted SDF string, checked and reviewed per host;
// clearing; and the read-only Soroban settings inspector.
//
// The screen does no I/O; it asks the root model with the messages in
// msgs.go.
package upgradesview

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
	"github.com/owenjacob-dev/auriga/internal/upgrades"
)

type phase int

const (
	phaseOverview phase = iota
	phaseTargets        // form 1: hosts, network, paste
	phaseFields         // form 2: the values
	phaseReview
	phaseSending
	phaseResults
	phaseClear
	phaseInspectForm
	phaseInspect
)

type fetched struct {
	scheduled map[string]string
	err       error
}

// Model is the screen.
type Model struct {
	// noColor drops colour from forms (NO_COLOR).
	noColor bool
	org     string
	fleet   *ui.Fleet
	now     func() time.Time
	loc     *time.Location
	phase   phase

	sched map[string]fetched

	// Arming.
	form     *huh.Form
	targets  []string
	network  string
	paste    string
	fields   map[string]*string // by parameter name, including "upgradetime" and "configupgradesetkey"
	params   upgrades.Params
	issues   []upgrades.Issue
	keyState string // "", "checking", "ok" or an error
	typed    string
	results  map[string]ResultMsg
	action   string // "arm" or "clear"

	// Inspector.
	inspectKey, inspectHost string
	inspect                 *InspectedMsg

	cols, rows, formHeight int
	scroll                 int
	flash                  string
}

// New opens the screen for org (all hosts when ""). now is the clock and
// loc the operator's time zone for the local time shown beside UTC.
// WithNoColor drops every colour from the screen's forms (NO_COLOR).
func (m *Model) WithNoColor(on bool) *Model { m.noColor = on; return m }

func New(org string, now func() time.Time, loc *time.Location) *Model {
	if loc == nil {
		loc = time.Local
	}
	return &Model{org: org, now: now, loc: loc, sched: map[string]fetched{}, results: map[string]ResultMsg{}}
}

// Action is "arm" or "clear" while sending and showing results.
func (m *Model) Action() string { return m.action }

// Params are the values being armed.
func (m *Model) Params() upgrades.Params { return m.params }

// Network is the network chosen on the form.
func (m *Model) Network() string { return m.network }

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// Init fetches every host's scheduled upgrades.
func (m *Model) Init() tea.Cmd { return m.refresh() }

func (m *Model) refresh() tea.Cmd {
	var aliases []string
	for _, h := range m.hosts() {
		aliases = append(aliases, h.Alias)
	}
	if len(aliases) == 0 {
		return nil
	}
	return ui.Emit(FetchMsg{Aliases: aliases})
}

// Busy implements ui.Screen.
func (m *Model) Busy() string {
	if m.phase == phaseSending {
		return "Upgrade votes are being sent to hosts."
	}
	return ""
}

// CapturingText implements ui.TextCapture.
func (m *Model) CapturingText() bool {
	switch m.phase {
	case phaseTargets, phaseFields, phaseInspectForm:
		return true
	case phaseReview, phaseClear:
		return m.network == "pubnet"
	}
	return false
}

func (m *Model) hosts() []*ui.HostView {
	if m.fleet == nil {
		return nil
	}
	return m.fleet.Hosts
}

// orgHosts are the hosts of the screen's org (all when it has none).
func (m *Model) orgHosts() []*ui.HostView {
	var out []*ui.HostView
	for _, h := range m.hosts() {
		if m.org == "" || h.Org == m.org {
			out = append(out, h)
		}
	}
	return out
}

func checkHost(h *ui.HostView) upgrades.Host {
	u := upgrades.Host{Alias: h.Alias, Org: h.Org, Network: h.Network}
	if i := h.Info; i != nil {
		u.Build, u.ProtocolVersion, u.LedgerVersion, u.Armed = shortBuild(i.Build), i.ProtocolVersion, i.Ledger.Version, i.Armed()
	}
	return u
}

var (
	kArm     = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "arm"))
	kClear   = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clear"))
	kInspect = key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "inspect settings"))
	kRearm   = key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "re-arm lost"))
	kRefresh = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh"))
	kBack    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kNext    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next"))
	kSend    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "send"))
	kEdit    = key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit"))
	kYes     = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "clear"))
	kDone    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "done"))
	kScroll  = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "scroll"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	switch m.phase {
	case phaseOverview:
		keys := []key.Binding{kArm, kClear, kInspect, kRefresh}
		if len(m.lost()) > 0 {
			keys = append([]key.Binding{kRearm}, keys...)
		}
		return append(keys, kBack)
	case phaseTargets, phaseFields, phaseInspectForm:
		return []key.Binding{kNext, kBack}
	case phaseReview:
		if upgrades.Blocking(m.issues) || m.keyState != "" && m.keyState != "ok" {
			return []key.Binding{kEdit, kBack}
		}
		return []key.Binding{kSend, kEdit, kBack}
	case phaseClear:
		return []key.Binding{kYes, kBack}
	case phaseSending:
		return nil
	case phaseInspect:
		return []key.Binding{kScroll, kDone}
	}
	return []key.Binding{kDone}
}

// lost lists hosts whose vote armed this session disappeared after a
// restart.
func (m *Model) lost() []*ui.HostView {
	var out []*ui.HostView
	for _, h := range m.hosts() {
		if h.ArmLost != nil {
			out = append(out, h)
		}
	}
	return out
}

func (m *Model) newForm(groups ...*huh.Group) {
	m.form = huh.NewForm(groups...).WithShowHelp(true).WithShowErrors(true).WithTheme(theme.Form(m.noColor))
	if m.cols > 0 {
		m.form = m.form.WithWidth(m.cols)
	}
	m.formHeight = 0
}

// startArm opens form 1: targets (the org's hosts), network, paste.
func (m *Model) startArm() tea.Cmd {
	m.phase, m.typed, m.paste, m.flash = phaseTargets, "", "", ""
	m.targets = nil
	m.network = ""
	var opts []huh.Option[string]
	for _, h := range m.hosts() {
		label := h.Alias
		if h.Org != "" {
			label += "  (" + h.Org + ")"
		}
		sel := m.org == "" || h.Org == m.org
		opts = append(opts, huh.NewOption(label, h.Alias).Selected(sel))
		if sel && m.network == "" {
			m.network = h.Network
		}
	}
	if m.network == "" {
		m.network = "testnet"
	}
	nets := []huh.Option[string]{huh.NewOption("Testnet", "testnet"), huh.NewOption("Pubnet", "pubnet")}
	for _, h := range m.hosts() {
		if h.Network != "" && h.Network != "testnet" && h.Network != "pubnet" {
			nets = append(nets, huh.NewOption(h.Network, h.Network))
			break
		}
	}
	m.newForm(huh.NewGroup(
		huh.NewMultiSelect[string]().Title("Hosts to arm").Description("space selects; default: the org's hosts").
			Options(opts...).Value(&m.targets).Height(min(len(opts), 6)+4),
		huh.NewSelect[string]().Title("Network").Options(nets...).Value(&m.network),
		huh.NewText().Title("Paste an arming string (optional)").
			Description("e.g. upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30").
			Value(&m.paste).Lines(2).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return nil
			}
			_, err := upgrades.Parse(s)
			return err
		}),
	))
	return m.form.Init()
}

// startFields opens form 2 with p's values.
func (m *Model) startFields(p upgrades.Params) tea.Cmd {
	m.phase = phaseFields
	m.fields = map[string]*string{}
	str := func(name, v string) *string { s := v; m.fields[name] = &s; return &s }
	t := ""
	if !p.Time.IsZero() {
		t = p.Time.UTC().Format(upgrades.TimeLayout)
	}
	fields := []huh.Field{huh.NewInput().Title("upgradetime (UTC)").Description("YYYY-MM-DDTHH:MM:SSZ; required").
		Value(str("upgradetime", t)).Validate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("upgradetime is required")
		}
		_, err := upgrades.ParseTime(s)
		return err
	})}
	for _, f := range upgrades.Fields {
		v := ""
		if n, ok := p.Get(f.Param); ok {
			v = strconv.FormatUint(uint64(n), 10)
		}
		param := f.Param
		desc := param
		if param == "expirationminutes" {
			desc += ": how long after upgradetime the vote may still apply (the build's default if empty)"
		}
		fields = append(fields, huh.NewInput().Title(f.Label).Description(desc).Value(str(param, v)).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return nil
			}
			_, err := upgrades.ParseUint32(param, s)
			return err
		}))
	}
	fields = append(fields, huh.NewInput().Title("configupgradesetkey").Description("Soroban settings proposal (base64)").
		Value(str("configupgradesetkey", p.ConfigUpgradeSetKey)).Validate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return upgrades.CheckKey(strings.TrimSpace(s))
	}))
	m.newForm(huh.NewGroup(fields...).Title("Values; empty fields are left out"))
	return m.form.Init()
}

// collect reads form 2 into params.
func (m *Model) collect() {
	p := upgrades.Params{Values: map[string]uint32{}}
	if t, err := upgrades.ParseTime(*m.fields["upgradetime"]); err == nil {
		p.Time = t
	}
	for _, f := range upgrades.Fields {
		if s := strings.TrimSpace(*m.fields[f.Param]); s != "" {
			if n, err := upgrades.ParseUint32(f.Param, s); err == nil {
				p.Values[f.Param] = n
			}
		}
	}
	p.ConfigUpgradeSetKey = strings.TrimSpace(*m.fields["configupgradesetkey"])
	m.params = p
}

func (m *Model) targetHosts() []upgrades.Host {
	var out []upgrades.Host
	for _, a := range m.targets {
		if h := m.fleet.Host(a); h != nil {
			out = append(out, checkHost(h))
		}
	}
	return out
}

// startReview runs the checks and, for a settings proposal, asks the first
// target to resolve the key.
func (m *Model) startReview() tea.Cmd {
	m.phase, m.typed, m.scroll = phaseReview, "", 0
	m.issues = upgrades.Check(m.params, m.network, m.targetHosts(), m.now())
	m.keyState = ""
	if m.params.ConfigUpgradeSetKey != "" && upgrades.CheckKey(m.params.ConfigUpgradeSetKey) == nil && len(m.targets) > 0 {
		m.keyState = "checking"
		return ui.Emit(ResolveKeyMsg{Alias: m.targets[0], Key: m.params.ConfigUpgradeSetKey})
	}
	return nil
}

func (m *Model) startClear() {
	m.phase, m.typed, m.targets, m.network = phaseClear, "", nil, ""
	for _, h := range m.orgHosts() {
		if (h.Info != nil && h.Info.Armed() != "") || len(m.sched[h.Alias].scheduled) > 0 {
			m.targets = append(m.targets, h.Alias)
			if m.network == "" {
				m.network = h.Network
			}
		}
	}
}

func (m *Model) startInspect() tea.Cmd {
	m.phase, m.inspect = phaseInspectForm, nil
	var opts []huh.Option[string]
	for _, h := range m.orgHosts() {
		opts = append(opts, huh.NewOption(h.Alias, h.Alias))
	}
	if len(opts) == 0 {
		m.phase, m.flash = phaseOverview, "No host to ask."
		return nil
	}
	if m.inspectHost == "" {
		m.inspectHost = opts[0].Value
	}
	m.newForm(huh.NewGroup(
		huh.NewInput().Title("ConfigUpgradeSetKey").Description("The proposal's key, as published before the vote (base64)").
			Value(&m.inspectKey).Validate(func(s string) error { return upgrades.CheckKey(strings.TrimSpace(s)) }),
		huh.NewSelect[string]().Title("Ask this host").Options(opts...).Value(&m.inspectHost),
	))
	return m.form.Init()
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case FetchedMsg:
		m.sched[msg.Alias] = fetched{msg.Scheduled, msg.Err}
		return m, nil
	case KeyResolvedMsg:
		if m.phase == phaseReview && msg.Key == m.params.ConfigUpgradeSetKey {
			m.keyState = "ok"
			if msg.Err != nil {
				m.keyState = msg.Err.Error()
			}
		}
		return m, nil
	case ResultMsg:
		m.results[msg.Alias] = msg
		if msg.Scheduled != nil {
			m.sched[msg.Alias] = fetched{scheduled: msg.Scheduled}
		}
		if len(m.results) == len(m.targets) {
			m.phase = phaseResults
		}
		return m, nil
	case InspectedMsg:
		if m.phase == phaseInspect && msg.Key == strings.TrimSpace(m.inspectKey) {
			m.inspect = &msg
		}
		return m, nil
	case tea.PasteMsg:
		if m.phase == phaseReview || m.phase == phaseClear {
			m.typed += msg.Content
			return m, nil
		}
	case tea.KeyPressMsg:
		if cmd, done := m.key(msg); done {
			return m, cmd
		}
	}
	if m.form != nil && (m.phase == phaseTargets || m.phase == phaseFields || m.phase == phaseInspectForm) {
		f, cmd := m.form.Update(msg)
		if hf, ok := f.(*huh.Form); ok {
			m.form = hf
		}
		if m.form.State == huh.StateCompleted {
			return m, tea.Batch(cmd, m.formDone())
		}
		return m, cmd
	}
	return m, nil
}

func (m *Model) formDone() tea.Cmd {
	switch m.phase {
	case phaseTargets:
		p := upgrades.Params{Values: map[string]uint32{}}
		if strings.TrimSpace(m.paste) != "" {
			p, _ = upgrades.Parse(m.paste) // validated by the field
		}
		return m.startFields(p)
	case phaseFields:
		m.collect()
		return m.startReview()
	case phaseInspectForm:
		m.phase = phaseInspect
		m.scroll = 0
		return ui.Emit(InspectMsg{Alias: m.inspectHost, Key: strings.TrimSpace(m.inspectKey)})
	}
	return nil
}

// key handles keys outside forms; done is false to pass them to a form.
func (m *Model) key(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := msg.String()
	switch m.phase {
	case phaseOverview:
		switch k {
		case "esc":
			return ui.Emit(ui.CloseMsg{}), true
		case "a":
			return m.startArm(), true
		case "R":
			lost := m.lost()
			if len(lost) == 0 {
				return nil, true
			}
			m.targets, m.network = nil, lost[0].Network
			for _, h := range lost {
				m.targets = append(m.targets, h.Alias)
			}
			return m.startFields(*lost[0].ArmLost), true
		case "c":
			m.startClear()
			if len(m.targets) == 0 {
				m.phase, m.flash = phaseOverview, "No host in this org is armed."
			}
			return nil, true
		case "i":
			return m.startInspect(), true
		case "r":
			return m.refresh(), true
		}
		return nil, true
	case phaseTargets, phaseFields, phaseInspectForm:
		if k == "esc" {
			if m.phase == phaseFields && len(m.targets) > 0 && m.paste != "" {
				return m.startArm(), true
			}
			m.phase = phaseOverview
			return nil, true
		}
		return nil, false
	case phaseReview:
		switch {
		case k == "esc":
			m.phase = phaseOverview
		case k == "e":
			if m.network != "pubnet" || m.typed == "" {
				return m.startFields(m.params), true
			}
			m.typed += "e"
		case k == "enter":
			if upgrades.Blocking(m.issues) || (m.keyState != "" && m.keyState != "ok") {
				return nil, true
			}
			if m.network == "pubnet" && strings.TrimSpace(m.typed) != "pubnet" {
				return nil, true
			}
			m.phase, m.action, m.results = phaseSending, "arm", map[string]ResultMsg{}
			return ui.Emit(ArmMsg{Network: m.network, Params: m.params, Aliases: slices.Clone(m.targets)}), true
		case k == "down" || k == "j" && m.network != "pubnet":
			m.scroll++
		case k == "up" || k == "k" && m.network != "pubnet":
			m.scroll = max(0, m.scroll-1)
		default:
			m.typeKey(msg)
		}
		return nil, true
	case phaseClear:
		switch k {
		case "esc":
			m.phase = phaseOverview
		case "enter":
			if m.network == "pubnet" && strings.TrimSpace(m.typed) != "pubnet" {
				return nil, true
			}
			was := map[string]string{}
			for _, a := range m.targets {
				if h := m.fleet.Host(a); h != nil && h.Info != nil {
					was[a] = h.Info.Armed()
				}
			}
			m.phase, m.action, m.results = phaseSending, "clear", map[string]ResultMsg{}
			return ui.Emit(ClearMsg{Aliases: slices.Clone(m.targets), Was: was}), true
		default:
			m.typeKey(msg)
		}
		return nil, true
	case phaseResults, phaseInspect:
		switch k {
		case "esc":
			m.phase, m.scroll = phaseOverview, 0
			return m.refresh(), true
		case "down", "j":
			m.scroll++
		case "up", "k":
			m.scroll = max(0, m.scroll-1)
		}
		return nil, true
	}
	return nil, true
}

func (m *Model) typeKey(msg tea.KeyPressMsg) {
	if m.network != "pubnet" {
		return
	}
	switch {
	case msg.String() == "backspace" && m.typed != "":
		_, n := utf8.DecodeLastRuneInString(m.typed)
		m.typed = m.typed[:len(m.typed)-n]
	case msg.Text != "":
		m.typed += msg.Text
	}
}

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	if m.fleet == nil {
		m.fleet = ctx.Fleet
	}
	t := ctx.Theme
	if m.cols != ctx.Cols || m.rows != ctx.Rows {
		m.cols, m.rows = ctx.Cols, ctx.Rows
		if m.form != nil {
			m.form = m.form.WithWidth(m.cols)
		}
	}
	sub := "upgrades"
	if m.org != "" {
		sub += " · org " + m.org
	}
	l := ui.Layout{Subtitle: sub}
	var body []string
	switch m.phase {
	case phaseOverview:
		body = m.overview(t, ctx.Cols)
		l.Status = "Values from info and upgrades?mode=get. Nothing is sent until you review it."
		if ctx.Fleet != nil && ctx.Fleet.Flash != "" {
			l.Status = ctx.Fleet.Flash
		}
		if m.flash != "" {
			l.Status = m.flash
		}
	case phaseTargets, phaseFields, phaseInspectForm:
		room := max(6, ctx.Rows)
		if room != m.formHeight {
			m.formHeight = room
			m.form = m.form.WithHeight(room)
		}
		body = strings.Split(strings.TrimRight(m.form.View(), "\n"), "\n")
		l.Status = map[phase]string{phaseTargets: "Arm · 1 of 3: hosts and network", phaseFields: "Arm · 2 of 3: values",
			phaseInspectForm: "Soroban settings inspector (read-only)"}[m.phase]
	case phaseReview:
		body = m.review(t, ctx.Cols)
		l.Status = "Arm · 3 of 3: review. Each host gets exactly this request."
	case phaseClear:
		body = m.clearView(t)
		l.Status = "upgrades?mode=clear removes the scheduled vote."
	case phaseSending, phaseResults:
		body = m.resultsView(t)
		if m.phase == phaseSending {
			l.Status = "Sending in parallel, then confirming with upgrades?mode=get and info…"
		} else {
			l.Status = "Every request is in the audit log."
		}
	case phaseInspect:
		body = m.inspectView(t, ctx.Cols)
		l.Status = "dumpproposedsettings compared with sorobaninfo?format=detailed; read-only."
	}
	if m.scroll > 0 && m.scroll < len(body) && m.phase != phaseTargets && m.phase != phaseFields && m.phase != phaseInspectForm {
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

func (m *Model) armedText(h *ui.HostView) (string, bool) {
	if h.Info != nil && h.Info.Armed() != "" {
		return armedValues(h.Info.Armed()), true
	}
	if f := m.sched[h.Alias]; len(f.scheduled) > 0 {
		return upgrades.FromScheduled(f.scheduled).String(), true
	}
	return "", false
}

// armedValues strips the "Armed with network upgrades: " prefix.
func armedValues(line string) string {
	_, v, ok := strings.Cut(line, ":")
	if !ok {
		return line
	}
	return strings.TrimSpace(v)
}

func (m *Model) overview(t theme.Theme, cols int) []string {
	hosts := m.orgHosts()
	head := fmt.Sprintf("%-10s %-7s %-8s %-6s %6s %9s  %s", "HOST", "NET", "BUILD", "PROTO", "FEE", "RESERVE", "ARMED")
	out := []string{t.Header.Render(components.Fit(head, cols))}
	if len(hosts) == 0 {
		return append(out, t.Muted.Render("No hosts yet. Add some on the dashboard (a)."))
	}
	var checks []upgrades.Host
	for _, h := range hosts {
		checks = append(checks, checkHost(h))
	}
	differ := map[string]bool{}
	for _, org := range upgrades.ArmedDifferently(checks) {
		differ[org] = true
	}
	for _, h := range hosts {
		net := h.Network
		if net == "" {
			net = "—"
		}
		build, proto, fee, reserve := "—", "—", "—", "—"
		if i := h.Info; i != nil {
			build = shortBuild(i.Build)
			proto = fmt.Sprintf("%d/%d", i.Ledger.Version, i.ProtocolVersion)
			fee, reserve = strconv.FormatInt(i.Ledger.BaseFee, 10), strconv.FormatInt(i.Ledger.BaseReserve, 10)
		}
		armed := t.Muted.Render("not armed")
		if text, ok := m.armedText(h); ok {
			armed = t.OK.Render("▲ " + strings.TrimPrefix(text, "upgrades?mode=set&"))
		}
		if f := m.sched[h.Alias]; f.err != nil {
			armed = t.Warn.Render("◐ upgrades?mode=get failed")
		}
		if h.ArmLost != nil {
			armed = t.Warn.Render("◐ vote lost; R re-arms")
		}
		if differ[h.Org] {
			armed = t.Warn.Render("◐ ") + armed
		}
		line := fmt.Sprintf("%-10s %-7s %-8s %-6s %6s %9s  ", components.Fit(h.Alias, 10), components.Fit(net, 7), components.Fit(build, 8), proto, fee, reserve)
		out = append(out, line+armed)
	}
	if len(differ) > 0 {
		out = append(out, "")
		for org := range differ {
			out = append(out, t.Warn.Render("◐ Hosts in "+org+" are armed differently."))
		}
	}
	return out
}

func shortBuild(b string) string {
	for _, f := range strings.Fields(b) {
		if len(f) > 0 && (f[0] == 'v' || f[0] >= '0' && f[0] <= '9') && strings.Count(f, ".") >= 2 {
			if f[0] != 'v' {
				f = "v" + f
			}
			return f
		}
	}
	return b
}

func (m *Model) when(t time.Time) string {
	d := t.Sub(m.now())
	rel := "in " + human(d)
	if d < 0 {
		rel = human(-d) + " ago"
	}
	return fmt.Sprintf("%s UTC (%s local, %s)", t.UTC().Format("2006-01-02 15:04:05"), t.In(m.loc).Format("2006-01-02 15:04 MST"), rel)
}

func (m *Model) review(t theme.Theme, cols int) []string {
	out := []string{t.Header.Render(fmt.Sprintf("Arm %d host(s) on %s", len(m.targets), m.network))}
	if !m.params.Time.IsZero() {
		out = append(out, "At "+m.when(m.params.Time))
	}
	out = append(out, "")
	for _, a := range m.targets {
		h := m.fleet.Host(a)
		build := ""
		if h != nil && h.Info != nil {
			build = fmt.Sprintf("  %s, protocol %d/%d", shortBuild(h.Info.Build), h.Info.Ledger.Version, h.Info.ProtocolVersion)
		}
		out = append(out, a+t.Muted.Render(build))
		out = append(out, "  "+m.params.String())
	}
	out = append(out, "")
	var blocks, warns []string
	for _, i := range m.issues {
		if i.Block {
			blocks = append(blocks, wrap(t.Bad.Render("○ "), i.String(), cols)...)
		} else {
			warns = append(warns, wrap(t.Warn.Render("◐ "), i.String(), cols)...)
		}
	}
	switch m.keyState {
	case "":
	case "checking":
		warns = append(warns, t.Muted.Render("· resolving configupgradesetkey with dumpproposedsettings…"))
	case "ok":
	default:
		blocks = append(blocks, wrap(t.Bad.Render("○ "), "configupgradesetkey does not resolve: "+m.keyState, cols)...)
	}
	out = append(out, blocks...)
	out = append(out, warns...)
	if len(blocks)+len(warns) > 0 {
		out = append(out, "")
	}
	switch {
	case len(blocks) > 0:
		out = append(out, "Fix these first (e edits the values).")
	case m.network == "pubnet":
		box := m.typed
		if box == "" {
			box = t.Muted.Render("…")
		}
		out = append(out, "Type "+t.Header.Render("pubnet")+" and press ⏎ to send: "+box)
	default:
		out = append(out, "Press ⏎ to send.")
	}
	return out
}

func (m *Model) clearView(t theme.Theme) []string {
	out := []string{t.Header.Render(fmt.Sprintf("Clear the upgrade votes of %d host(s)", len(m.targets))), ""}
	for _, a := range m.targets {
		text := "armed"
		if h := m.fleet.Host(a); h != nil {
			text, _ = m.armedText(h)
		}
		out = append(out, fmt.Sprintf("  %-10s %s", a, text))
	}
	out = append(out, "")
	if m.network == "pubnet" {
		out = append(out, "Type "+t.Header.Render("pubnet")+" and press ⏎ to clear: "+m.typed)
	} else {
		out = append(out, "Press ⏎ to clear.")
	}
	return out
}

func (m *Model) resultsView(t theme.Theme) []string {
	verb := map[string]string{"arm": "Arming", "clear": "Clearing"}[m.action]
	out := []string{t.Header.Render(verb + " " + strconv.Itoa(len(m.targets)) + " host(s)"), ""}
	for _, a := range m.targets {
		r, ok := m.results[a]
		switch {
		case !ok:
			out = append(out, t.Muted.Render("· "+a+" waiting"))
		case r.Err != nil:
			out = append(out, t.Bad.Render("○ "+a+" failed: ")+firstLine(r.Err.Error()))
		case !r.Confirmed:
			out = append(out, t.Warn.Render("◐ "+a+" sent, but upgrades?mode=get does not show it"))
		case m.action == "clear":
			out = append(out, t.OK.Render("● "+a+" cleared"))
		default:
			out = append(out, t.OK.Render("● "+a+" armed and confirmed"))
			if r.Armed != "" {
				out = append(out, t.Muted.Render("    info: "+armedValues(r.Armed)))
			}
		}
	}
	return out
}

func (m *Model) inspectView(t theme.Theme, cols int) []string {
	out := []string{t.Header.Render("Proposal " + strings.TrimSpace(m.inspectKey)), t.Muted.Render("on " + m.inspectHost), ""}
	r := m.inspect
	switch {
	case r == nil:
		return append(out, t.Muted.Render("Asking the host…"))
	case r.Err != nil:
		return append(out, t.Bad.Render("○ ")+r.Err.Error())
	case len(r.Changes) == 0:
		return append(out, t.OK.Render("● The proposal changes nothing: every value equals the current setting."))
	}
	out = append(out, fmt.Sprintf("%d value(s) would change:", len(r.Changes)), "")
	sort.SliceStable(r.Changes, func(i, j int) bool { return r.Changes[i].Setting < r.Changes[j].Setting })
	last := ""
	for _, c := range r.Changes {
		if c.Setting != last {
			out = append(out, t.Header.Render(strings.TrimPrefix(c.Setting, "CONFIG_SETTING_")))
			last = c.Setting
		}
		old := c.Old
		if old == "" {
			old = "(not set)"
		}
		out = append(out, fmt.Sprintf("  %s: %s → %s", c.Field, t.Bad.Render(old), t.OK.Render(c.New)))
	}
	return out
}

// human is a duration as "2d 3h", "3h 5m" or "12m".
func human(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

// wrap breaks text into lines of at most cols, the first after prefix
// (a two-cell symbol) and the rest indented to match.
func wrap(prefix, text string, cols int) []string {
	width := max(20, cols-2)
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) > width {
			out = append(out, line)
			line = w
			continue
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	out = append(out, line)
	for i := range out {
		if i == 0 {
			out[i] = prefix + out[i]
		} else {
			out[i] = "  " + out[i]
		}
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
