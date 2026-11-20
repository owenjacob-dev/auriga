package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(out, "[Config]") || !strings.Contains(out, "● Adoptable") || strings.Contains(out, "SDHO") {
		t.Errorf("config tab:\n%s", out)
	}
	if !strings.Contains(ansi.Strip(step(t, m, keyPress("pgdown")).Render()), `NODE_SEED="<redacted:GBRP`) {
		t.Error("redacted file not shown")
	}
}

func TestDetailConfigDiffAndIssues(t *testing.T) {
	b := newFakeBackend(testInventory("acme-a"))
	b.facts["acme-a"] = healthyFacts
	b.facts["acme-a"] = func() remote.ProbeFacts { f := healthyFacts; f.ServiceWorkDir = "/"; return f }()
	b.configs["acme-a"] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, Text: []byte(`# hand written
NETWORK_PASSPHRASE="Test SDF Network ; September 2015"
NODE_SEED="<redacted:` + pubA + `> acme-a"
NODE_IS_VALIDATOR=true
DATABASE="sqlite3://stellar.db"
CATCHUP_RECENT=1024
`)}
	b.backups["acme-a"] = []remote.Backup{{Path: "/etc/stellar/stellar-core.cfg.auriga-20261102T131004Z.bak", Time: time.Date(2026, 11, 2, 13, 10, 4, 0, time.UTC), Size: 812, Owner: "root:stellar", Mode: "640"}}
	m := startModel(t, b)
	m = step(t, m, connected("acme-a"))
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	for range 4 {
		m = tab(t, m)
	}
	out := ansi.Strip(m.Render())
	for _, want := range []string{
		"● Adoptable", "◐ LOG_FILE_PATH: the default log file path resolves against /", "◐ DATABASE: the SQLite file",
		"2026-11-02 13:10:04Z  root:stellar 640  812 bytes",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config tab lacks %q", want)
		}
	}
	t.Run("file", func(t *testing.T) { golden.RequireEqual(t, out) })
	m = step(t, m, keyPress("d"))
	out = ansi.Strip(m.Render())
	for _, want := range []string{"What a first deploy changes", "1 comment line will be dropped", "1 other setting is kept unchanged: CATCHUP_RECENT"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff view lacks %q:\n%s", want, out)
		}
	}
	t.Run("diff", func(t *testing.T) { golden.RequireEqual(t, out) })
	if out := ansi.Strip(step(t, m, keyPress("pgdown")).Render()); !strings.Contains(out, "+++ rendered by auriga") {
		t.Errorf("diff body not reachable:\n%s", out)
	}
}

func TestReadOnlyAndPublicPortOnDashboard(t *testing.T) {
	b := newFakeBackend(testInventory("legacy", "open"))
	b.facts["legacy"], b.facts["open"] = healthyFacts, healthyFacts
	for _, a := range []string{"legacy", "open"} {
		b.admin[a] = map[string][]byte{"info": coreFixture(t, "recorded/stellar-core-30.0.0/info.json")}
	}
	b.configs["legacy"] = remote.RedactedConfig{Text: []byte("NETWORK_PASSPHRASE=\"x\"\n[QUORUM_SET]\nTHRESHOLD_PERCENT=67\n")}
	b.configs["open"] = remote.RedactedConfig{Text: append(cfgText("acme.org", "Test SDF Network ; September 2015"), []byte("PUBLIC_HTTP_PORT=true\n")...)}
	// The fixture already sets PUBLIC_HTTP_PORT=false; replace it.
	b.configs["open"] = remote.RedactedConfig{Text: []byte(strings.Replace(string(cfgText("acme.org", "Test SDF Network ; September 2015")), "PUBLIC_HTTP_PORT=false", "PUBLIC_HTTP_PORT=true", 1))}
	m := startModel(t, b)
	m = step(t, m, connected("legacy"))
	m = step(t, m, connected("open"))
	out := ansi.Strip(m.Render())
	if !strings.Contains(out, "config is read-only: uses the legacy") {
		t.Errorf("legacy status:\n%s", out)
	}
	m = step(t, m, keyPress("j"))
	out = ansi.Strip(m.Render())
	if !strings.Contains(out, "PUBLIC_HTTP_PORT=true: the admin endpoint is open") || !strings.Contains(out, "○ open") {
		t.Errorf("public port not red:\n%s", out)
	}
}

// TestDetailQuorumTabUsesWidth checks that a tiered quorum set spreads into
// columns on wider terminals, beside the summary once there is room, and
// that names reported by stellar-core are shown whole.
func TestDetailQuorumTabUsesWidth(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 30}, {Width: 120, Height: 30}, {Width: 200, Height: 30}} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			b := newFakeBackend(testInventory("acme-a", "acme-b"))
			b.facts["acme-a"], b.facts["acme-b"] = healthyFacts, healthyFacts
			b.admin["acme-a"] = map[string][]byte{
				"info":   coreFixture(t, "synthetic/info-pubnet-synced.json"),
				"quorum": coreFixture(t, "synthetic/quorum-tiered-named.json"),
			}
			m := startModel(t, b)
			m = step(t, m, size)
			m = step(t, m, connected("acme-a"))
			m = step(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
			out := ansi.Strip(m.Render())
			for _, want := range []string{"[Quorum]", "Quorum set  4 of 5", "epsilon-backup-1", "fail_with     beta-1, gamma-1"} {
				if !strings.Contains(out, want) {
					t.Errorf("lacks %q", want)
				}
			}
			golden.RequireEqual(t, out)
		})
	}
}

func TestDetailLeftRightSwitchTabs(t *testing.T) {
	m, _ := detailModel(t)
	for _, k := range []struct {
		code rune
		want string
	}{{tea.KeyRight, "[Quorum]"}, {tea.KeyRight, "[Peers]"}, {tea.KeyLeft, "[Quorum]"}, {tea.KeyLeft, "[Info]"}, {tea.KeyLeft, "[Probe]"}} {
		m = step(t, m, tea.KeyPressMsg{Code: k.code})
		if out := ansi.Strip(m.Render()); !strings.Contains(out, k.want) {
			t.Fatalf("after %v want %s:\n%s", k.code, k.want, out)
		}
	}
}

// TestDetailPeersTabWide checks that peers show whole keys when the table
// fits the terminal.
func TestDetailPeersTabWide(t *testing.T) {
	m, _ := detailModel(t)
	m = step(t, m, tea.WindowSizeMsg{Width: 140, Height: 24})
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	out := ansi.Strip(m.Render())
	for _, want := range []string{"acme-b (GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q)", "GDVEU3DD4KOFECV66VIHWEZOYX4ZKR3WV27L464SIIPOU2IUI3JCZA57"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q", want)
		}
	}
	golden.RequireEqual(t, out)
}
