package wizard

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/quorum"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/strkey"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

type phase int

const (
	phaseLoading phase = iota
	phaseForm
	phaseSecret  // pasting a seed to import
	phaseRefresh // reviewing a preset refresh
	phaseReview
)

// Model is the setup wizard screen.
type Model struct {
	// noColor drops colour from forms (NO_COLOR).
	noColor bool
	draft   *Draft
	step    int
	phase   phase
	form    *huh.Form
	fs      formState
	preset  *presets.Preset
	plan    *Plan
	fleet   *ui.Fleet
	version string
	now     func() time.Time

	// Imported seeds live only here, in wiped buffers; the draft keeps
	// their public keys.
	seeds     map[string]*secret.Buffer
	secretFor string
	secretIn  *components.SecretInput
	secretErr string

	archives map[string]archiveResult
	refresh  *presets.Preset
	refDiff  string
	flash    string

	reviewHost int
	reviewDiff bool
	scroll     int
	// tried marks steps the operator tried to complete; their blocking
	// issues are shown only after that.
	tried map[int]bool
	// checking lists hosts whose archive check is still running.
	checking map[string]bool

	cols, rows int
	formHeight int
}

// New opens the wizard for org (a new org when empty), preselecting hosts.
// WithNoColor drops every colour from the wizard's forms (NO_COLOR).
func (m *Model) WithNoColor(on bool) *Model { m.noColor = on; return m }

func New(org string, hosts []string, version string, now func() time.Time) *Model {
	d := NewDraft()
	d.OrgName, d.Hosts = org, hosts
	return &Model{draft: d, step: StepScope, phase: phaseLoading, version: version, now: now,
		seeds: map[string]*secret.Buffer{}, secretIn: components.NewSecretInput(), archives: map[string]archiveResult{},
		tried: map[int]bool{}, checking: map[string]bool{}}
}

// SetFleet implements ui.FleetAware.
func (m *Model) SetFleet(f *ui.Fleet) { m.fleet = f }

// Draft returns the current answers (for tests and the deploy flow).
func (m *Model) Draft() *Draft { return m.draft }

// Plan returns the latest plan.
func (m *Model) Plan() *Plan { return m.plan }

// Seeds returns the imported seeds, for the deploy flow to send over stdin.
func (m *Model) Seeds() map[string]*secret.Buffer { return m.seeds }

// Close wipes imported seeds and anything typed but not submitted.
func (m *Model) Close() {
	for _, b := range m.seeds {
		b.Zero()
	}
	m.secretIn.Clear()
}

// Init asks for the saved draft and the preset.
func (m *Model) Init() tea.Cmd {
	return ui.Emit(LoadMsg{Org: m.draft.OrgName, Network: presets.Network(m.draft.Network)})
}

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

// CapturingText implements ui.TextCapture: forms and the seed field take
// every key but ctrl+c.
func (m *Model) CapturingText() bool { return m.phase == phaseForm || m.phase == phaseSecret }

var (
	kNext    = key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next"))
	kBack    = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	kRefresh = key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "refresh preset"))
	kHost    = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next host"))
	kDiff    = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff"))
	kDeploy  = key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "deploy"))
	kYes     = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "save"))
	kNo      = key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "discard"))
)

// Keys implements ui.Screen.
func (m *Model) Keys() []key.Binding {
	switch m.phase {
	case phaseReview:
		if m.plan != nil && len(m.plan.Hosts) > 0 && !m.plan.Blocking(m.draft.Quorum.UnsafeConsent) {
			return []key.Binding{kDeploy, kHost, kDiff, kBack}
		}
		return []key.Binding{kHost, kDiff, kBack}
	case phaseRefresh:
		return []key.Binding{kYes, kNo}
	case phaseSecret:
		return []key.Binding{kNext, kBack}
	}
	if m.step == StepQuorum && m.preset != nil {
		return []key.Binding{kNext, kRefresh, kBack}
	}
	return []key.Binding{kNext, kBack}
}

// prefill starts a new draft from what the hosts already run: home
// domain, network and node type of the first adopted host, and each
// host's own archive.
func (m *Model) prefill() {
	first := true
	facts := m.facts()
	for _, h := range m.draft.Hosts {
		c := facts[h].Existing
		if c == nil {
			continue
		}
		var own *stellarcfg.HistoryArchive
		for i, a := range c.History {
			if a.Put != "" && own == nil {
				own = &c.History[i]
			}
		}
		if own != nil {
			if m.draft.Archives == nil {
				m.draft.Archives = map[string]stellarcfg.ArchiveSpec{}
			}
			m.draft.Archives[h] = stellarcfg.ArchiveFromHistory(*own)
		}
		if !first {
			continue
		}
		first = false
		if m.draft.HomeDomain == "" {
			m.draft.HomeDomain = c.NodeHomeDomain
		}
		for net, pass := range Passphrases {
			if c.NetworkPassphrase == pass {
				m.draft.Network = net
			}
		}
		switch validator := c.NodeIsValidator != nil && *c.NodeIsValidator; {
		case validator && own != nil:
			m.draft.NodeType = TypeFull
		case validator:
			m.draft.NodeType = TypeBasic
		case own != nil:
			m.draft.NodeType = TypeArchiver
		default:
			m.draft.NodeType = TypeWatcher
		}
	}
}

// archiveResult is an archive check and the spec it checked.
type archiveResult struct {
	ArchiveCheckedMsg
	spec stellarcfg.ArchiveSpec
}

// facts gathers what the fleet knows about the draft's hosts.
func (m *Model) facts() map[string]HostFacts {
	out := map[string]HostFacts{}
	for _, a := range m.draft.Hosts {
		h := m.fleet.Host(a)
		if h == nil {
			continue
		}
		f := HostFacts{ArchiveExists: "unknown"}
		if h.Class != nil && h.Class.Result != nil && h.Class.Class.String() == "adoptable" {
			f.Existing = h.Class.Result.Config
		}
		if h.Config != nil {
			f.SeedPublicKey = h.Config.PublicKey
			if f.Existing != nil {
				f.ExistingText = h.Config.Text
			}
		}
		if h.Probe != nil {
			f.Running = h.Probe.ServiceActive == "active"
			f.ServiceWorkDir = h.Probe.ServiceWorkDir
		}
		if c, ok := m.archives[a]; ok && c.Err == nil && c.Check.Exists != "" && c.spec == m.draft.Archives[a] {
			f.ArchiveExists = c.Check.Exists
		}
		out[a] = f
	}
	return out
}

func (m *Model) replan() {
	p := m.preset
	if !m.draft.Quorum.UsePreset {
		p = nil
	}
	m.plan = BuildPlan(Input{Draft: m.draft, Facts: m.facts(), Preset: p,
		Render: stellarcfg.RenderOptions{Version: m.version, Time: m.now()}})
	// Missing programs found by the archive check block the history step.
	for h, c := range m.archives {
		if c.Err == nil && len(c.Check.Missing) > 0 && Publishes(m.draft.NodeType) && c.spec == m.draft.Archives[h] {
			m.plan.Issues = append(m.plan.Issues, Issue{Step: StepHistory, Host: h, Rule: "archive-programs", Severity: quorum.Block,
				Message: "the stellar user cannot run " + strings.Join(c.Check.Missing, ", ") + " on this host; install it or choose another backend"})
		}
	}
}

func (m *Model) needsConsent() bool { return len(m.consentRules()) > 0 }

// consentRules lists the rules that need UNSAFE_QUORUM in the current plan.
func (m *Model) consentRules() map[string]bool {
	m.replan()
	out := map[string]bool{}
	for _, i := range m.plan.Issues {
		if ConsentRule(i.Rule) {
			out[i.Rule] = true
		}
	}
	return out
}

// hostsChangingNetwork lists adopted hosts whose config is on another
// network than the draft's.
func (m *Model) hostsChangingNetwork() []string {
	var out []string
	want := Passphrases[m.draft.Network]
	for h, f := range m.facts() {
		if f.Existing != nil && f.Existing.NetworkPassphrase != "" && f.Existing.NetworkPassphrase != want {
			out = append(out, h)
		}
	}
	return out
}

// goTo opens step s (skipping History for types that do not publish).
func (m *Model) goTo(s int) tea.Cmd {
	if s == StepHistory && !Publishes(m.draft.NodeType) {
		if s > m.step {
			s++
		} else {
			s--
		}
	}
	m.step, m.flash, m.scroll = s, "", 0
	m.draft.Step = s
	m.replan()
	if s == StepReview {
		m.phase, m.reviewHost = phaseReview, 0
		return nil
	}
	m.phase = phaseForm
	m.form = m.buildForm()
	return m.form.Init()
}

func (m *Model) save() tea.Cmd {
	if !orgNameRE.MatchString(m.draft.OrgName) {
		return nil
	}
	cp := *m.draft
	return ui.Emit(SaveDraftMsg{Draft: &cp})
}

// stepDone validates the completed step and moves on, or stays with the
// step's blocking issues listed.
func (m *Model) stepDone() tea.Cmd {
	m.collect()
	m.replan()
	m.tried[m.step] = true
	if m.step == StepNetwork {
		return tea.Batch(m.save(), ui.Emit(LoadPresetMsg{Network: presets.Network(m.draft.Network)}), m.goTo(m.step+1))
	}
	if m.step == StepKeys {
		if h := m.nextImport(); h != "" {
			m.startSecret(h)
			return nil
		}
	}
	var cmds []tea.Cmd
	if m.step == StepHistory {
		// Check each archive on its host (programs, existence) before moving
		// on; results for unchanged specs are reused.
		for _, h := range m.draft.Hosts {
			a, ok := m.draft.Archives[h]
			if !ok || a.Validate() != nil || a.URL == "" {
				continue // nothing to check against (a custom archive without a URL)
			}
			if c, done := m.archives[h]; done && c.spec == a {
				continue
			}
			m.checking[h] = true
			cmds = append(cmds, ui.Emit(CheckArchiveMsg{Alias: h, Spec: a, Publish: true}))
		}
		if len(m.checking) > 0 {
			m.flash = "Checking the archives on the hosts…"
			return tea.Batch(cmds...)
		}
	}
	consent := m.draft.Quorum.UnsafeConsent
	for _, i := range m.plan.IssuesFor(m.step) {
		if i.Severity == quorum.Block || (i.Severity == quorum.NeedsConsent && !consent) {
			m.form = m.buildForm()
			m.flash = "Fix the problems below to continue."
			return tea.Batch(append(cmds, m.form.Init())...)
		}
	}
	return tea.Batch(append(cmds, m.save(), m.goTo(m.step+1))...)
}

// nextImport returns a host set to import whose seed is not in memory.
func (m *Model) nextImport() string {
	for _, h := range m.draft.Hosts {
		if m.draft.Keys[h].Mode == KeyImport && m.seeds[h] == nil {
			return h
		}
	}
	return ""
}

func (m *Model) startSecret(h string) {
	m.phase, m.secretFor, m.secretErr = phaseSecret, h, ""
	m.secretIn.Clear()
}

// Update implements ui.Screen.
func (m *Model) Update(msg tea.Msg) (ui.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case LoadedMsg:
		resumed := msg.Draft != nil
		if resumed {
			m.draft = msg.Draft
		} else {
			m.prefill()
		}
		cmd := m.goTo(max(StepScope, min(m.draft.Step, StepAdvanced)))
		if resumed {
			m.flash = "Resumed your saved draft."
		}
		return m, tea.Batch(ui.Emit(LoadPresetMsg{Network: presets.Network(m.draft.Network)}), cmd)
	case tea.PasteMsg:
		if m.phase == phaseSecret {
			m.secretIn.Update(msg)
			m.secretErr = ""
			return m, nil
		}
	case PresetMsg:
		if msg.Err != nil && m.draft.Network == NetworkPrivate {
			m.preset, m.draft.Quorum.UsePreset = nil, false // no preset for the test network
		} else if msg.Err != nil {
			m.flash = "Preset not loaded: " + msg.Err.Error()
		} else {
			m.preset = msg.Preset
		}
		m.replan()
		return m, nil
	case ArchiveCheckedMsg:
		res := archiveResult{ArchiveCheckedMsg: msg, spec: m.draft.Archives[msg.Alias]}
		m.archives[msg.Alias] = res
		delete(m.checking, msg.Alias)
		m.replan()
		if msg.Err != nil {
			m.flash = msg.Alias + ": archive check failed: " + msg.Err.Error()
		}
		if len(m.checking) == 0 && m.step == StepHistory && m.phase == phaseForm && m.form.State == huh.StateCompleted {
			return m, m.stepDone()
		}
		return m, nil
	case FetchedPresetMsg:
		return m, m.fetched(msg)
	case tea.WindowSizeMsg:
		m.cols, m.rows = msg.Width-components.FrameChromeCols, msg.Height-components.FrameChromeRows
	case tea.KeyPressMsg:
		switch m.phase {
		case phaseSecret:
			return m, m.secretKey(msg)
		case phaseRefresh:
			return m, m.refreshKey(msg)
		case phaseReview:
			return m, m.reviewKey(msg)
		case phaseForm:
			switch msg.String() {
			case "esc":
				if m.step == StepScope {
					return m, tea.Batch(m.save(), ui.Emit(ui.CloseMsg{}))
				}
				m.collect()
				return m, tea.Batch(m.save(), m.goTo(m.step-1))
			case "ctrl+r":
				if m.step == StepQuorum && m.preset != nil {
					m.flash = "Fetching " + presets.SourceURL(m.preset.Network) + " …"
					return m, ui.Emit(FetchPresetMsg{Network: m.preset.Network})
				}
			}
		}
	}
	if m.phase != phaseForm || m.form == nil {
		return m, nil
	}
	next, cmd := m.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		m.form = f
	}
	if m.form.State == huh.StateCompleted {
		return m, tea.Batch(cmd, m.stepDone())
	}
	return m, cmd
}

func (m *Model) secretKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Code {
	case tea.KeyEscape:
		m.secretIn.Clear()
		return m.goTo(StepKeys)
	case tea.KeyEnter:
		buf := m.secretIn.Take()
		pk, err := strkey.PublicKeyFromSeed(buf.Bytes())
		if err != nil {
			buf.Zero()
			m.secretErr = "That is not a valid secret seed (S…, 56 characters). Nothing was kept."
			return nil
		}
		h := m.secretFor
		if old := m.seeds[h]; old != nil {
			old.Zero()
		}
		m.seeds[h] = buf
		k := m.draft.Keys[h]
		k.ImportedPublicKey = pk
		m.draft.Keys[h] = k
		if next := m.nextImport(); next != "" {
			m.startSecret(next)
			return nil
		}
		m.phase = phaseForm
		return m.stepDone()
	}
	if m.secretIn.Update(msg) {
		m.secretErr = ""
	}
	return nil
}

func (m *Model) fetched(msg FetchedPresetMsg) tea.Cmd {
	if msg.Err != nil {
		m.flash = "Preset refresh failed: " + msg.Err.Error()
		return nil
	}
	p, err := presets.Extract(msg.Network, presets.SourceURL(msg.Network), m.now().UTC().Format("2006-01-02"), msg.Body)
	if err != nil {
		m.flash = "The fetched file is not a usable preset: " + err.Error()
		return nil
	}
	oldB, _ := m.preset.Encode()
	newB, _ := p.Encode()
	m.refresh, m.refDiff = p, stellarcfg.Diff("current preset", "fetched now", oldB, newB)
	m.phase, m.scroll = phaseRefresh, 0
	return nil
}

func (m *Model) refreshKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "y":
		p := m.refresh
		p.Overridden = true
		m.preset, m.refresh = p, nil
		m.flash = "Preset updated: " + p.Summary()
		m.phase = phaseForm
		m.form = m.buildForm()
		return tea.Batch(ui.Emit(SavePresetMsg{Preset: p}), m.form.Init())
	case "n", "esc":
		m.refresh, m.phase = nil, phaseForm
		return nil
	case "down", "j":
		m.scroll++
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	}
	return nil
}

func (m *Model) reviewKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		return m.goTo(StepAdvanced)
	case "tab":
		m.reviewHost = (m.reviewHost + 1) % max(1, len(m.plan.Hosts))
		m.scroll = 0
	case "d":
		m.reviewDiff = !m.reviewDiff
		m.scroll = 0
	case "D":
		return m.deploy()
	case "down", "j":
		m.scroll++
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "pgdown":
		m.scroll += 10
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	}
	return nil
}

// deploy hands the reviewed plan to the deploy screen. Nothing on a host
// has changed until the operator confirms there.
func (m *Model) deploy() tea.Cmd {
	m.replan()
	if m.plan == nil || len(m.plan.Hosts) == 0 || m.plan.Blocking(m.draft.Quorum.UnsafeConsent) {
		return nil
	}
	seeds := map[string]*secret.Buffer{}
	for h, b := range m.seeds {
		if m.draft.Keys[h].Mode == KeyImport && b.Len() > 0 {
			seeds[h] = secret.FromBytes(append([]byte(nil), b.Bytes()...))
		}
	}
	in := Input{Draft: m.draft.Clone(), Facts: m.facts(), Render: stellarcfg.RenderOptions{Version: m.version, Time: m.now()}}
	if m.draft.Quorum.UsePreset {
		in.Preset = m.preset
	}
	replan := func(generated map[string]string) *Plan {
		in := in
		in.Generated = generated
		return BuildPlan(in)
	}
	return ui.Emit(DeployMsg{Plan: replan(nil), Seeds: seeds, Replan: replan})
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
	body := []string{m.stepBar(t), ""}
	switch m.phase {
	case phaseLoading:
		body = append(body, t.Muted.Render("Loading…"))
	case phaseSecret:
		body = append(body, m.secretView(t)...)
	case phaseRefresh:
		body = append(body, m.refreshView(t, ctx)...)
	case phaseReview:
		body = append(body, m.reviewView(t, ctx)...)
	case phaseForm:
		issues := m.issueLines(t, ctx.Cols)
		if len(issues) > ctx.Rows/2 {
			issues = issues[:ctx.Rows/2]
		}
		room := max(6, ctx.Rows-len(body)-len(issues))
		if room != m.formHeight {
			m.formHeight = room
			m.form = m.form.WithHeight(room)
		}
		formLines := strings.Split(strings.TrimRight(m.form.View(), "\n"), "\n")
		if len(formLines) > room {
			formLines = formLines[:room]
		}
		body = append(body, formLines...)
		body = append(body, issues...)
	}
	status := t.Muted.Render(fmt.Sprintf("Step %d of %d · %s · draft saved after each step", m.step, NumSteps, StepNames[m.step]))
	if m.flash != "" {
		status = t.Warn.Render(m.flash)
	}
	sub := "setup"
	if m.draft.OrgName != "" {
		sub = "setup " + m.draft.OrgName
	}
	return ui.Layout{Subtitle: sub, Body: body, Status: status}
}

func (m *Model) stepBar(t theme.Theme) string {
	var parts []string
	for s := StepScope; s <= NumSteps; s++ {
		name := fmt.Sprintf("%d %s", s, StepNames[s])
		switch {
		case s == StepHistory && !Publishes(m.draft.NodeType):
			parts = append(parts, t.Stale.Render(fmt.Sprintf("%d –", s)))
		case s == m.step:
			parts = append(parts, t.Header.Reverse(true).Render("["+name+"]"))
		case s < m.step:
			parts = append(parts, t.OK.Render(fmt.Sprintf("%d ✓", s)))
		default:
			parts = append(parts, t.Muted.Render(fmt.Sprint(s)))
		}
	}
	return strings.Join(parts, " ")
}

// issueLines lists the current step's issues; at the quorum step they come
// with the preview.
func (m *Model) issueLines(t theme.Theme, cols int) []string {
	if m.plan == nil {
		return nil
	}
	// Problems first: the quorum preview can be long and is cut to fit.
	// The same finding on several hosts is shown once, naming them.
	type entry struct {
		hosts []string
		sev   quorum.Severity
	}
	var order []string
	byText := map[string]*entry{}
	for _, i := range m.plan.IssuesFor(m.step) {
		if (i.Severity == quorum.Block || i.Severity == quorum.NeedsConsent) && !m.tried[m.step] {
			continue
		}
		if i.Severity == quorum.NeedsConsent && m.draft.Quorum.UnsafeConsent {
			continue
		}
		e, ok := byText[i.Message]
		if !ok {
			e = &entry{sev: i.Severity}
			byText[i.Message] = e
			order = append(order, i.Message)
		}
		if i.Host != "" {
			e.hosts = append(e.hosts, i.Host)
		}
	}
	var out []string
	for _, text := range order {
		e := byText[text]
		if len(e.hosts) > 0 && len(m.draft.Hosts) > 1 {
			text = strings.Join(e.hosts, ", ") + ": " + text
		}
		mark := t.Warn.Render("◐ ")
		if e.sev != quorum.Warn {
			mark = t.Bad.Render("○ ")
		}
		out = append(out, components.Fit(mark+text, cols))
	}
	if len(out) > 0 {
		out = append([]string{""}, out...)
	}
	if m.step == StepQuorum && len(m.plan.Preview) > 0 {
		out = append(out, "", t.Header.Render("Preview of the quorum set stellar-core will build (for "+m.plan.Hosts[0].Alias+")"))
		out = append(out, m.plan.Preview...)
	}
	return out
}

func (m *Model) secretView(t theme.Theme) []string {
	out := []string{
		t.Header.Render("Seed to import for " + m.secretFor),
		"",
		"Paste the secret seed (S…). It is checked here, kept in memory only,",
		"and sent to " + m.secretFor + " over stdin during the deploy. It is never written to disk.",
		"",
		"  " + m.secretIn.View(40),
	}
	if m.secretErr != "" {
		out = append(out, "", t.Bad.Render("○ "+m.secretErr))
	}
	return out
}

func (m *Model) refreshView(t theme.Theme, ctx ui.Context) []string {
	out := []string{t.Header.Render("Fetched " + presets.SourceURL(m.refresh.Network)), m.refresh.Summary(), ""}
	if m.refDiff == "" {
		out = append(out, t.OK.Render("● No changes from the current preset."))
	} else {
		out = append(out, colourDiff(t, m.refDiff)...)
	}
	return scrolled(out, &m.scroll, ctx.Rows-2)
}

func colourDiff(t theme.Theme, diff string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
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

func scrolled(lines []string, scroll *int, rows int) []string {
	if len(lines) > rows {
		*scroll = min(*scroll, len(lines)-rows)
	} else {
		*scroll = 0
	}
	return lines[*scroll:]
}

func (m *Model) reviewView(t theme.Theme, ctx ui.Context) []string {
	p := m.plan
	if p == nil || len(p.Hosts) == 0 {
		return []string{t.Muted.Render("Nothing to review.")}
	}
	var out []string
	if p.Blocking(m.draft.Quorum.UnsafeConsent) {
		out = append(out, t.Bad.Render("○ Not ready: go back (esc) and fix these:"))
		for _, i := range p.Issues {
			if i.Severity == quorum.Block || (i.Severity == quorum.NeedsConsent && !m.draft.Quorum.UnsafeConsent) {
				out = append(out, components.Fit("  "+i.String(), ctx.Cols))
			}
		}
	} else {
		out = append(out, t.OK.Render(fmt.Sprintf("● Ready: %d hosts on %s. Nothing has changed on any host.", len(p.Hosts), p.Network)))
		out = append(out, t.Muted.Render("D opens the deploy: it lists every step and asks before changing anything."))
	}
	hp := p.Hosts[m.reviewHost%len(p.Hosts)]
	var tabs []string
	for i, h := range p.Hosts {
		if i == m.reviewHost%len(p.Hosts) {
			tabs = append(tabs, t.Header.Reverse(true).Render("["+h.Alias+"]"))
		} else {
			tabs = append(tabs, " "+h.Alias+" ")
		}
	}
	out = append(out, "", strings.Join(tabs, ""))
	label := "new node"
	if !hp.New {
		label = "adopted"
	}
	seed := map[stellarcfg.SeedMode]string{stellarcfg.SeedKeep: "keep the existing seed", stellarcfg.SeedGenerate: "generate a seed on the host", stellarcfg.SeedImport: "import the pasted seed"}[hp.Seed.Mode]
	out = append(out, fmt.Sprintf("%s · %s · %s", label, hp.NodeType, seed))
	for _, a := range hp.Actions {
		what := a.Kind
		if a.Archive != "" {
			what += " " + a.Archive
		}
		out = append(out, components.Fit(t.Warn.Render("◐ will run "+what+": "+a.Reason+" (a rollback cannot undo it)"), ctx.Cols))
	}
	if len(hp.Preview) > 0 {
		q := "quorum: " + strings.TrimSpace(hp.Preview[0])
		if hp.OwnQuorum {
			q += " · its own set (step 7)"
		}
		out = append(out, components.Fit(q, ctx.Cols))
	}
	if len(hp.Pending) > 0 {
		out = append(out, t.Muted.Render("Keys of "+strings.Join(hp.Pending, ", ")+" are generated during the deploy and filled in then."))
	}
	for _, i := range p.Issues {
		if i.Host == hp.Alias && i.Severity == quorum.Warn {
			out = append(out, components.Fit(t.Warn.Render("◐ ")+i.Message, ctx.Cols))
		}
	}
	out = append(out, "")
	if m.reviewDiff && hp.Diff != "" {
		out = append(out, colourDiff(t, hp.Diff)...)
	} else if m.reviewDiff {
		out = append(out, t.Muted.Render("No diff: this is a new node. d shows the file."))
	} else {
		out = append(out, strings.Split(strings.TrimRight(string(hp.Rendered), "\n"), "\n")...)
	}
	return scrolled(out, &m.scroll, ctx.Rows-2)
}
