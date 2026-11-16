package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/owenjacob-dev/auriga/internal/remote"
)

func detailModel(t *testing.T) (Model, *fakeBackend) {
	t.Helper()
	b := newFakeBackend(testInventory("acme-a", "acme-b"))
	b.facts["acme-a"], b.facts["acme-b"] = healthyFacts, healthyFacts
	b.configs["acme-a"] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, Text: cfgText("acme.org", "Public Global Stellar Network ; September 2015")}
	b.configs["acme-b"] = remote.RedactedConfig{PublicKey: pubB, Seed: remote.SeedPresent, Text: cfgText("acme.org", "Public Global Stellar Network ; September 2015")}
	b.admin["acme-a"] = map[string][]byte{
		"info":   coreFixture(t, "synthetic/info-pubnet-synced.json"),
		"quorum": coreFixture(t, "synthetic/quorum-full.json"),
		"peers":  coreFixture(t, "synthetic/peers.json"),
	}
	m := startModel(t, b)
	m = step(t, m, connected("acme-a"))
	m = step(t, m, connected("acme-b"))
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	return m, b
}

func tab(t *testing.T, m Model) Model { return step(t, m, tea.KeyPressMsg{Code: tea.KeyTab}) }

func TestDetailInfoTab(t *testing.T) {
	m, _ := detailModel(t)
	out := ansi.Strip(m.Render())
	if !strings.Contains(out, "[Info]") || !strings.Contains(out, "State     Synced!") || !strings.Contains(out, "fail_at 3, intersection yes") {
		t.Fatalf("info tab:\n%s", out)
	}
	golden.RequireEqual(t, out)
}

func TestDetailQuorumTab(t *testing.T) {
	m, b := detailModel(t)
	m = tab(t, m)
	out := ansi.Strip(m.Render())
	for _, want := range []string{
		"fail_at       1", "fail_with     acme-b (GAB2…QZ6Q)", "Missing       1", "    GDVE…ZA57",
		"Intersection  ● yes", "Critical", "2 of 2", "acme-a (GBRP…OX2H)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quorum tab lacks %q", want)
		}
	}
	golden.RequireEqual(t, out)
	if out := ansi.Strip(step(t, m, keyPress("pgdown")).Render()); !strings.Contains(out, "1 of 2") {
		t.Errorf("nested set not reachable by scrolling:\n%s", out)
	}
	// t runs the transitive check.
	calls := len(b.adminCalls)
	step(t, m, keyPress("t"))
	if got := b.adminCalls[len(b.adminCalls)-1]; len(b.adminCalls) != calls+1 || got != "acme-a quorum?compact=false&transitive=true" {
		t.Errorf("transitive call %v", b.adminCalls[calls:])
	}
}

func TestDetailPeersTab(t *testing.T) {
	m, _ := detailModel(t)
	m = tab(t, tab(t, m))
	out := ansi.Strip(m.Render())
	for _, want := range []string{"in  acme-b (GAB2…QZ6Q)", "10.0.0.2:52110", "12ms", "out GDVE…ZA57", "85ms", "pending", "2 authenticated (1 in, 1 out), 1 pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("peers tab lacks %q", want)
		}
	}
	golden.RequireEqual(t, out)
}

func TestDetailFetchError(t *testing.T) {
	m, _ := detailModel(t)
	m = tab(t, tab(t, tab(t, m))) // Upgrades: not in the fake, so 404
	if out := ansi.Strip(m.Render()); !strings.Contains(out, "admin upgrades?mode=get: HTTP 404") {
		t.Errorf("upgrades error:\n%s", out)
	}
}

func TestDetailCopyJSON(t *testing.T) {
	m, _ := detailModel(t)
	tm, cmd := m.Update(keyPress("c"))
	m = tm.(Model)
	if cmd == nil {
		t.Fatal("c did nothing")
	}
	if !strings.Contains(ansi.Strip(m.Render()), "Copied info JSON") {
		t.Error("no confirmation")
	}
}

func TestDetailConfigTabIsRedacted(t *testing.T) {
	m, _ := detailModel(t)
	for range 4 {
		m = tab(t, m)
	}
	out := ansi.Strip(m.Render())
	if !strings.Contains(out, "[Config]") || !strings.Contains(out, `NODE_SEED="<redacted:GBRP`) || strings.Contains(out, "SDHO") {
		t.Errorf("config tab:\n%s", out)
	}
}
