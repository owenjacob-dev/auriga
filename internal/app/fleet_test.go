package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
)

const (
	pubA = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	pubB = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
)

// fakeBackend records calls; probes return canned results.
type fakeBackend struct {
	mu        sync.Mutex
	inv       *inventory.Inventory
	saved     []*inventory.Inventory
	connected []string
	removed   []string
	reconnect []string
	aliases   []string
	facts     map[string]remote.ProbeFacts
	configs   map[string]remote.RedactedConfig
	probeErr  map[string]error
	events    chan sshx.Event
	closed    bool
}

func newFakeBackend(inv *inventory.Inventory) *fakeBackend {
	return &fakeBackend{
		inv: inv, events: make(chan sshx.Event),
		facts: map[string]remote.ProbeFacts{}, configs: map[string]remote.RedactedConfig{}, probeErr: map[string]error{},
	}
}

func (f *fakeBackend) Events() <-chan sshx.Event { return f.events }
func (f *fakeBackend) ListAliases() ([]string, string, error) {
	return f.aliases, "", nil
}
func (f *fakeBackend) Connect(a string) {
	f.mu.Lock()
	f.connected = append(f.connected, a)
	f.mu.Unlock()
}
func (f *fakeBackend) Disconnect(a string) {
	f.mu.Lock()
	f.removed = append(f.removed, a)
	f.mu.Unlock()
}
func (f *fakeBackend) Reconnect(a string) {
	f.mu.Lock()
	f.reconnect = append(f.reconnect, a)
	f.mu.Unlock()
}
func (f *fakeBackend) Probe(_ context.Context, a, _, _ string) (remote.ProbeFacts, error) {
	return f.facts[a], f.probeErr[a]
}
func (f *fakeBackend) ReadConfig(_ context.Context, a, _ string) (remote.RedactedConfig, error) {
	return f.configs[a], nil
}
func (f *fakeBackend) LoadInventory() (*inventory.Inventory, error) {
	if f.inv == nil {
		return nil, errors.New("broken file")
	}
	return f.inv, nil
}
func (f *fakeBackend) SaveInventory(inv *inventory.Inventory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *inv
	cp.Hosts = append([]inventory.Host(nil), inv.Hosts...)
	cp.Orgs = append([]inventory.Org(nil), inv.Orgs...)
	f.saved = append(f.saved, &cp)
	return nil
}
func (f *fakeBackend) Close() { f.closed = true }

func (f *fakeBackend) lastSaved() *inventory.Inventory {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saved) == 0 {
		return nil
	}
	return f.saved[len(f.saved)-1]
}

// step feeds msg to the model and runs the resulting commands to completion,
// feeding their messages back in. Commands that block (the pool listener)
// are abandoned after a short wait.
func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	tm, cmd := m.Update(msg)
	m = tm.(Model)
	return run(t, m, cmd)
}

func run(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(100 * time.Millisecond):
		return m // a blocking listener
	}
	switch msg := msg.(type) {
	case nil:
		return m
	case tea.BatchMsg:
		for _, c := range msg {
			m = run(t, m, c)
		}
		return m
	case tea.QuitMsg:
		return m
	default:
		return step(t, m, msg)
	}
}

func testInventory(aliases ...string) *inventory.Inventory {
	inv := inventory.New()
	for _, a := range aliases {
		inv.AddHost(a)
	}
	return inv
}

func startModel(t *testing.T, b *fakeBackend) Model {
	t.Helper()
	m := New(Options{Now: fixedNow, DisableClock: true, Backend: b, NoColor: true})
	m = run(t, m, m.Init())
	return step(t, m, size80x24)
}

func cfgText(home, pass string) []byte {
	return []byte(`NETWORK_PASSPHRASE="` + pass + `"
NODE_SEED="<redacted:` + pubA + `> acme-a"
NODE_HOME_DOMAIN="` + home + `"
`)
}

func connected(alias string) poolEventMsg {
	return poolEventMsg{sshx.StateEvent{HostState: sshx.HostState{Alias: alias, State: sshx.StateConnected, Address: "10.0.0.1:22", Route: []string{alias}}}}
}

func TestLoadConnectProbeAndGroup(t *testing.T) {
	b := newFakeBackend(testInventory("acme-a", "acme-b", "acme-c"))
	healthy := remote.ProbeFacts{
		CoreInstalled: true, CoreVersion: "stellar-core 30.0.0 (abc)", ServiceActive: "active", ServiceEnabled: "enabled",
		ServiceLoad: "loaded", ConfigPresent: true, ConfigReadable: true, ConfigMode: "640", HTTPPort: 11626,
		DiskAvailKB: 120 << 20, DiskTotalKB: 200 << 20, NTPSync: "yes",
	}
	b.facts["acme-a"] = healthy
	b.configs["acme-a"] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, NodeAlias: "acme-a", Text: cfgText("acme.org", "Public Global Stellar Network ; September 2015")}
	lowDisk := healthy
	lowDisk.DiskAvailKB = 5 << 20
	lowDisk.ConfigMode = "644"
	b.facts["acme-b"] = lowDisk
	b.configs["acme-b"] = remote.RedactedConfig{PublicKey: pubB, Seed: remote.SeedPresent, Text: cfgText("acme.org", "Public Global Stellar Network ; September 2015")}

	m := startModel(t, b)
	if !reflect.DeepEqual(b.connected, []string{"acme-a", "acme-b", "acme-c"}) {
		t.Fatalf("connected %v", b.connected)
	}
	m = step(t, m, connected("acme-a"))
	m = step(t, m, connected("acme-b"))
	m = step(t, m, poolEventMsg{sshx.StateEvent{HostState: sshx.HostState{
		Alias: "acme-c", State: sshx.StateAuthFailed, Waiting: true,
		Err: "authentication as ops@acme-c failed (agent present; tried agent ssh-ed25519 SHA256:x)",
	}}})

	saved := b.lastSaved()
	if saved == nil {
		t.Fatal("inventory not saved after probe")
	}
	if h := saved.Host("acme-a"); h.PublicKey != pubA || h.Org != "acme.org" || h.NodeName != "acme-a" || h.HTTPPort != 11626 {
		t.Errorf("acme-a saved as %+v", h)
	}
	if saved.Host("acme-b").Org != "acme.org" || len(saved.Orgs) != 1 || saved.Orgs[0].Network != "pubnet" {
		t.Errorf("grouping: %+v", saved)
	}
	golden.RequireEqual(t, ansi.Strip(m.Render()))

	// The status line follows the selection.
	m = step(t, m, keyPress("j"))
	m = step(t, m, keyPress("j"))
	if out := ansi.Strip(m.Render()); !strings.Contains(out, "acme-c · ssh auth failed") || !strings.Contains(out, "press r to retry") {
		t.Errorf("status line for acme-c:\n%s", out)
	}
	// r on a waiting host reconnects instead of probing.
	m = step(t, m, keyPress("r"))
	if !reflect.DeepEqual(b.reconnect, []string{"acme-c"}) {
		t.Errorf("reconnect %v", b.reconnect)
	}
	// Detail view for the selected host.
	m = step(t, m, keyPress("k"))
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	out := ansi.Strip(m.Render())
	for _, want := range []string{"Node public key   " + pubB, "config is world-readable (mode 644)", "only 5G free"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail lacks %q:\n%s", want, out)
		}
	}
	m = step(t, m, keyPress("esc"))
	if len(m.stack) != 1 {
		t.Error("esc did not close the detail view")
	}
}

func TestProbeFailureShown(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	b.probeErr["a"] = &remote.SudoError{Alias: "a", Detail: "a password is required"}
	m := startModel(t, b)
	m = step(t, m, connected("a"))
	if hv := m.fleet.Host("a"); hv.ProbeErr == "" || hv.Probing {
		t.Fatalf("%+v", hv)
	}
	if out := ansi.Strip(m.Render()); !strings.Contains(out, "probe failed") {
		t.Errorf("status:\n%s", out)
	}
}

func TestHostKeyPrompt(t *testing.T) {
	b := newFakeBackend(testInventory("a", "b"))
	m := startModel(t, b)
	p1, answer1 := sshx.NewPromptForTest(sshx.PromptEvent{Alias: "a", HostKey: &sshx.HostKeyQuestion{
		Alias: "a", Address: "10.0.0.1:22", KeyType: "ssh-ed25519", Fingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", SaveTo: "/home/op/.ssh/known_hosts",
	}})
	p2, answer2 := sshx.NewPromptForTest(sshx.PromptEvent{Alias: "b", HostKey: &sshx.HostKeyQuestion{Alias: "b", KeyType: "ssh-rsa", Fingerprint: "SHA256:x"}})
	m = step(t, m, poolEventMsg{p1})
	m = step(t, m, poolEventMsg{p2})
	golden.RequireEqual(t, ansi.Strip(m.Render()))

	// q does not quit while a prompt is open; it is not a prompt answer.
	tm, cmd := m.Update(keyPress("q"))
	m = tm.(Model)
	if cmd != nil {
		t.Fatal("q acted during a prompt")
	}
	m = step(t, m, keyPress("t"))
	if a, ok := answer1(); !ok || a.HostKey != sshx.HostKeyTrustSession {
		t.Errorf("answer1 %+v %v", a, ok)
	}
	m = step(t, m, keyPress("r"))
	if a, ok := answer2(); !ok || a.HostKey != sshx.HostKeyReject {
		t.Errorf("answer2 %+v %v", a, ok)
	}
	if len(m.prompts) != 0 {
		t.Error("prompts left")
	}
}

func TestPassphrasePromptMasksInput(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	m := startModel(t, b)
	p, answer := sshx.NewPromptForTest(sshx.PromptEvent{Alias: "a", Sudo: true})
	m = step(t, m, poolEventMsg{p})
	for _, r := range "s3cr?t q" {
		m = step(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = step(t, m, tea.PasteMsg{Content: "XY\n"})
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	out := m.Render()
	if strings.Contains(out, "s3cr") || strings.Contains(out, "XY") {
		t.Fatal("secret rendered")
	}
	golden.RequireEqual(t, ansi.Strip(out))
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a, ok := answer()
	if !ok || a.Secret == nil || string(a.Secret.Bytes()) != "s3cr?t qX" {
		t.Fatalf("answer %v %v", ok, a)
	}
	if m.secretIn.Len() != 0 {
		t.Error("input not cleared")
	}
	// esc cancels the next one.
	p2, answer2 := sshx.NewPromptForTest(sshx.PromptEvent{Alias: "a", KeyPath: "/home/op/.ssh/id_ed25519", Retry: true})
	m = step(t, m, poolEventMsg{p2})
	m = step(t, m, keyPress("esc"))
	if a, ok := answer2(); !ok || !a.Cancelled {
		t.Errorf("esc: %+v %v", a, ok)
	}
}

func TestPickerAddsHosts(t *testing.T) {
	b := newFakeBackend(testInventory("acme-a"))
	b.aliases = []string{"acme-a", "acme-b", "acme-c", "quorum-x"}
	m := startModel(t, b)
	m = step(t, m, keyPress("a"))
	if len(m.stack) != 2 {
		t.Fatal("picker not open")
	}
	golden.RequireEqual(t, ansi.Strip(m.Render()))
	// Filter with "/" then type text containing q and ?: must not quit.
	m = step(t, m, keyPress("/"))
	for _, r := range "acme" {
		m = step(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // leave filter
	m = step(t, m, keyPress("j"))                       // acme-b (acme-a is already added)
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.stack) != 1 {
		t.Fatal("picker still open")
	}
	if !reflect.DeepEqual(b.connected, []string{"acme-a", "acme-b", "acme-c"}) {
		t.Errorf("connected %v", b.connected)
	}
	if s := b.lastSaved(); s == nil || len(s.Hosts) != 3 {
		t.Errorf("saved %+v", s)
	}
}

func TestPickerFilterKeepsQ(t *testing.T) {
	b := newFakeBackend(testInventory())
	b.aliases = []string{"quorum-x", "acme-a"}
	m := startModel(t, b)
	m = step(t, m, keyPress("a"))
	m = step(t, m, keyPress("/"))
	tm, cmd := m.Update(keyPress("q"))
	m = tm.(Model)
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("q quit while typing a filter")
		}
	}
	if !strings.Contains(ansi.Strip(m.Render()), "Filter: q") {
		t.Error("q not typed into the filter")
	}
}

func TestRemoveHost(t *testing.T) {
	b := newFakeBackend(testInventory("a", "b"))
	m := startModel(t, b)
	m = step(t, m, keyPress("x"))
	if !strings.Contains(ansi.Strip(m.Render()), "Remove a from the inventory? Nothing on the host changes.") {
		t.Fatal("no confirmation")
	}
	m = step(t, m, keyPress("n"))
	if len(b.removed) != 0 {
		t.Fatal("removed without confirmation")
	}
	m = step(t, m, keyPress("x"))
	m = step(t, m, keyPress("y"))
	if !reflect.DeepEqual(b.removed, []string{"a"}) || len(m.fleet.Hosts) != 1 {
		t.Errorf("removed %v, fleet %d", b.removed, len(m.fleet.Hosts))
	}
	if s := b.lastSaved(); s == nil || s.Host("a") != nil {
		t.Error("removal not saved")
	}
}

func TestBrokenInventoryIsNeverOverwritten(t *testing.T) {
	b := newFakeBackend(nil)
	b.aliases = []string{"x"}
	m := startModel(t, b)
	if !strings.Contains(ansi.Strip(m.Render()), "Inventory not loaded: broken file") {
		t.Errorf("no warning:\n%s", ansi.Strip(m.Render()))
	}
	step(t, m, ui.AddHostsMsg{Aliases: []string{"x"}})
	if len(b.saved) != 0 || len(b.connected) != 0 {
		t.Error("acted on a broken inventory")
	}
}

func TestCloseCancelsPromptsAndBackend(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	m := startModel(t, b)
	p, answer := sshx.NewPromptForTest(sshx.PromptEvent{Alias: "a", Sudo: true})
	m = step(t, m, poolEventMsg{p})
	m = step(t, m, keyPress("z"))
	m.Close()
	if a, ok := answer(); !ok || !a.Cancelled {
		t.Error("pending prompt not cancelled")
	}
	if !b.closed {
		t.Error("backend not closed")
	}
}
