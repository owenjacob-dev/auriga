package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/pelletier/go-toml/v2"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

// Public test-vector seeds whose keys the fixture org uses.
var wizardSeeds = map[string]string{
	"acme-a": "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4",
	"acme-b": "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI",
	"acme-c": "SADQOBYHA4DQOBYHA4DQOBYHA4DQOBYHA4DQOBYHA4DQOBYHA4DQP54X",
}

func keys(t *testing.T, m Model, ks ...string) Model {
	t.Helper()
	for _, k := range ks {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		default:
			for _, r := range k {
				m = step(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			continue
		}
		m = step(t, m, msg)
	}
	return m
}

func screen(m Model) string { return ansi.Strip(m.Render()) }

func wizardModel(t *testing.T) (Model, *fakeBackend) {
	t.Helper()
	cmdWait = 15 * time.Millisecond
	t.Cleanup(func() { cmdWait = 100 * time.Millisecond })
	b := newFakeBackend(testInventory("acme-a", "acme-b", "acme-c"))
	newNode := healthyFacts
	newNode.ConfigPresent, newNode.ServiceActive, newNode.ServiceWorkDir = false, "inactive", "/"
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		b.facts[h] = newNode
	}
	m := startModel(t, b)
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		m = step(t, m, connected(h))
	}
	return keys(t, m, "s"), b
}

// TestWizardTestnetBasicEndToEnd walks the eight steps for the fixture org
// "testnet Basic": three new hosts, imported seeds, SDF testnet preset,
// consent for zero fault tolerance.
func TestWizardTestnetBasicEndToEnd(t *testing.T) {
	m, b := wizardModel(t)
	out := screen(m)
	if !strings.Contains(out, "[1 Scope]") || !strings.Contains(out, "Organisation name") {
		t.Fatalf("wizard did not open on the scope step:\n%s", out)
	}
	t.Run("scope", func(t *testing.T) { golden.RequireEqual(t, out) })

	// 1 Scope: name prefilled from the host alias; add the other hosts.
	m = keys(t, m, "enter", "acme.example", "enter", "down", "space", "down", "space", "enter")
	if !strings.Contains(screen(m), "[2 Network]") {
		t.Fatalf("not on network:\n%s", screen(m))
	}
	// 2 Network: testnet. 3 Node type: Basic.
	m = keys(t, m, "enter", "enter")
	if !strings.Contains(screen(m), "[4 Keys]") {
		t.Fatalf("not on keys:\n%s", screen(m))
	}
	// 4 Keys: import for each host, with its public address.
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		m = keys(t, m, "down", "enter", "enter", "core-"+h[5:]+".acme.example:11625", "enter", "enter")
	}
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		out := screen(m)
		if !strings.Contains(out, "Seed to import for "+h) {
			t.Fatalf("no seed prompt for %s:\n%s", h, out)
		}
		if h == "acme-a" {
			m = step(t, m, tea.PasteMsg{Content: "SNOTAVALIDSEED"})
			m = keys(t, m, "enter")
			if !strings.Contains(screen(m), "not a valid secret seed") {
				t.Fatal("bad seed accepted")
			}
		}
		m = step(t, m, tea.PasteMsg{Content: wizardSeeds[h] + "\n"})
		if strings.Contains(m.Render(), wizardSeeds[h]) {
			t.Fatal("seed rendered on screen")
		}
		if h == "acme-a" {
			t.Run("seed", func(t *testing.T) { golden.RequireEqual(t, screen(m)) })
		}
		m = keys(t, m, "enter")
	}
	out = screen(m)
	if !strings.Contains(out, "[5 Quorum]") || !strings.Contains(out, "Preview of the quorum set") || !strings.Contains(out, "sdftest1") {
		t.Fatalf("quorum step:\n%s", out)
	}
	t.Run("quorum", func(t *testing.T) { golden.RequireEqual(t, out) })

	// 5 Quorum: MEDIUM, use the preset, nothing pasted. The set tolerates no
	// failures, so the step stays until the consent box is ticked.
	m = keys(t, m, "enter", "enter", "enter")
	if out := screen(m); !strings.Contains(out, "Accept a quorum set that tolerates no failures?") {
		t.Fatalf("no consent page:\n%s", out)
	}
	m = keys(t, m, "enter") // answer No
	if out := screen(m); !strings.Contains(out, "[5 Quorum]") || !strings.Contains(out, "Fix the problems below") || !strings.Contains(out, "tolerates no failures") {
		t.Fatalf("zero tolerance did not stop the step:\n%s", out)
	}
	m = keys(t, m, "enter", "enter", "enter", "y")
	out = screen(m)
	if !strings.Contains(out, "[7 Advanced]") || !strings.Contains(out, "6 –") {
		t.Fatalf("history not skipped for Basic validators:\n%s", out)
	}
	// 7 Advanced: a database for each new node, defaults for the rest; no
	// quorum override for acme-a and acme-b.
	for range 2 {
		m = keys(t, m, "sqlite3:///var/lib/stellar/stellar.db", "enter", "enter", "enter", "enter", "enter", "enter", "enter", "enter")
	}
	// acme-c takes its own quorum set: the editor opens with the org-wide
	// entries, kept as they are here.
	m = keys(t, m, "sqlite3:///var/lib/stellar/stellar.db", "enter", "enter", "enter", "enter", "enter", "enter", "enter", "y")
	if out := screen(m); !strings.Contains(out, "acme-c: own quorum set") || !strings.Contains(out, "sdftest1") {
		t.Fatalf("override editor not shown with the org-wide entries:\n%s", out)
	}
	t.Run("override", func(t *testing.T) { golden.RequireEqual(t, screen(m)) })
	m = keys(t, m, "enter")
	out = screen(m)
	if !strings.Contains(out, "[8 Review]") || !strings.Contains(out, "● Ready: 3 hosts on testnet. Nothing has changed on any host.") {
		t.Fatalf("review:\n%s", out)
	}
	t.Run("review", func(t *testing.T) { golden.RequireEqual(t, out) })
	m = keys(t, m, "tab", "tab")
	if out := screen(m); !strings.Contains(out, "[acme-c]") || !strings.Contains(out, "its own set (step 7)") {
		t.Errorf("review does not mark acme-c's own quorum set:\n%s", out)
	}

	w := m.top().(*wizard.Model)
	plan := w.Plan()
	if plan.Blocking(true) || len(plan.Hosts) != 3 {
		t.Fatalf("plan %+v", plan.Issues)
	}
	for _, hp := range plan.Hosts {
		if !bytes.Contains(hp.Rendered, []byte("<auriga-seed:import:G")) || !bytes.Contains(hp.Rendered, []byte("UNSAFE_QUORUM = true")) {
			t.Errorf("%s render:\n%s", hp.Alias, hp.Rendered)
		}
	}
	if len(w.Seeds()) != 3 {
		t.Errorf("seeds in memory: %d", len(w.Seeds()))
	}

	// Drafts were saved along the way, without a seed.
	if b.draftSaves < 6 {
		t.Errorf("draft saved %d times", b.draftSaves)
	}
	enc, _ := toml.Marshal(b.drafts["acme"])
	for _, s := range wizardSeeds {
		if bytes.Contains(enc, []byte(s)) {
			t.Fatal("a seed reached the draft")
		}
	}
	if !bytes.Contains(enc, []byte("GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H")) {
		t.Error("imported public key not saved")
	}

	if d := b.drafts["acme"]; !d.Advanced["acme-c"].QuorumOverride || !strings.Contains(d.Advanced["acme-c"].QuorumEntries, "sdftest1") || d.Advanced["acme-a"].QuorumOverride {
		t.Errorf("override not saved: %+v", d.Advanced)
	}

	// Leaving the wizard wipes the seeds.
	seeds := w.Seeds()
	m = keys(t, m, "esc", "esc", "esc", "esc", "esc", "esc", "esc")
	if len(m.stack) != 1 {
		t.Fatalf("wizard still open: %d screens", len(m.stack))
	}
	for h, s := range seeds {
		if s.Len() != 0 {
			t.Errorf("%s seed not wiped", h)
		}
	}
}

func TestWizardResumesDraftAndHistoryChecks(t *testing.T) {
	cmdWait = 15 * time.Millisecond
	t.Cleanup(func() { cmdWait = 100 * time.Millisecond })
	b := newFakeBackend(testInventory("acme-a", "acme-b", "acme-c"))
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		f := healthyFacts
		f.ConfigPresent, f.ServiceActive, f.ServiceWorkDir = false, "inactive", "/"
		b.facts[h] = f
	}
	d := wizard.NewDraft()
	d.OrgName, d.HomeDomain, d.Hosts, d.NodeType, d.Step = "acme", "acme.example", []string{"acme-a", "acme-b", "acme-c"}, wizard.TypeFull, wizard.StepHistory
	for _, h := range d.Hosts {
		d.Keys[h] = wizard.KeyInput{Mode: wizard.KeyGenerate, Alias: h}
	}
	b.drafts["acme"] = d
	b.archiveChecks["acme-b"] = remote.ArchiveCheck{Missing: []string{"aws"}, Exists: "no"}
	m := startModel(t, b)
	for _, h := range d.Hosts {
		m = step(t, m, connected(h))
	}
	m = keys(t, m, "s")
	out := screen(m)
	if !strings.Contains(out, "[6 History]") || !strings.Contains(out, "Resumed your saved draft") {
		t.Fatalf("not resumed at History:\n%s", out)
	}
	t.Run("history", func(t *testing.T) { golden.RequireEqual(t, out) })
	// S3 for each host: name, backend, then URL, bucket, prefix, endpoint.
	for _, h := range d.Hosts {
		m = keys(t, m, "enter", "enter", "https://history-"+h[5:]+".acme.example", "enter", "acme-history", "enter", "enter", "enter")
	}
	out = screen(m)
	if !strings.Contains(out, "[6 History]") || !strings.Contains(out, "cannot run aws") {
		t.Fatalf("missing aws on acme-b did not block:\n%s", out)
	}
}

func TestWizardKeepsAdoptedAlias(t *testing.T) {
	cmdWait = 15 * time.Millisecond
	t.Cleanup(func() { cmdWait = 100 * time.Millisecond })
	b := newFakeBackend(testInventory("host-1"))
	b.facts["host-1"] = healthyFacts
	b.configs["host-1"] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, NodeAlias: "acme-a",
		Text: cfgText("acme.example", "Test SDF Network ; September 2015")}
	m := startModel(t, b)
	m = step(t, m, connected("host-1"))
	// Scope (home domain prefilled from the host), network, node type.
	m = keys(t, m, "s", "enter", "enter", "enter", "enter", "enter")
	if out := screen(m); !strings.Contains(out, "[4 Keys]") || !strings.Contains(out, "> acme-a") || !strings.Contains(out, "Keep the existing seed") {
		t.Fatalf("alias not taken from NODE_SEED:\n%s", out)
	}
}
