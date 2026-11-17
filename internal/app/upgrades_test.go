package app

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/upgrades"
)

// fakeCore is a stellar-core node's admin endpoint as far as upgrades go,
// answering like stellar-core 30.0.0 (recorded on the integration stack):
// set and clear answer with an empty body, errors with HTTP 200 and text,
// get with {"data": X, "nullopt": false} for set values.
type fakeCore struct {
	protocol, ledgerVersion int
	baseFee, baseReserve    int64
	startedOn               string
	armed                   url.Values // the last set, nil when not armed
	calls                   []string
	network                 string
	soroban                 []byte
	proposals               map[string]string
}

func newCore() *fakeCore {
	return &fakeCore{protocol: 30, ledgerVersion: 29, baseFee: 100, baseReserve: 5000000, startedOn: "2026-11-14T10:00:00Z",
		network: "Test SDF Network ; September 2015", proposals: map[string]string{}}
}

func (c *fakeCore) do(cmd corehttp.Command) (int, []byte, error) {
	c.calls = append(c.calls, cmd.String())
	q, _ := url.ParseQuery(strings.TrimPrefix(cmd.String(), cmd.Name()+"?"))
	switch {
	case cmd.Name() == "info":
		status := "[]"
		if c.armed != nil {
			var parts []string
			for _, f := range []string{"upgradetime", "protocolversion", "basefee"} {
				if v := c.armed.Get(f); v != "" {
					parts = append(parts, f+"="+v)
				}
			}
			status = `["Armed with network upgrades: ` + strings.Join(parts, ", ") + `"]`
		}
		return 200, fmt.Appendf(nil, `{"info":{"build":"stellar-core 30.0.0 (abc)","network":%q,
			"protocol_version":%d,"state":"Synced!","startedOn":%q,"status":%s,
			"ledger":{"num":4242,"age":2,"version":%d,"baseFee":%d,"baseReserve":%d,"closeTime":1795453300}}}`,
			c.network, c.protocol, c.startedOn, status, c.ledgerVersion, c.baseFee, c.baseReserve), nil
	case cmd.Name() == "sorobaninfo":
		return 200, c.soroban, nil
	case cmd.Name() == "dumpproposedsettings":
		if p, ok := c.proposals[q.Get("blob")]; ok {
			return 200, []byte(p), nil
		}
		return 200, []byte("configUpgradeSet is missing or invalid"), nil
	case cmd.Name() == "upgrades" && q.Get("mode") == "set":
		if v, _ := strconv.Atoi(q.Get("protocolversion")); v > c.protocol {
			return 200, fmt.Appendf(nil, `{"exception": "Protocol version error: supported is up to %d, passed is %d"}`, c.protocol, v), nil
		}
		c.armed = q
		return 200, nil, nil
	case cmd.Name() == "upgrades" && q.Get("mode") == "clear":
		c.armed = nil
		return 200, nil, nil
	case cmd.Name() == "upgrades":
		opt := func(param string) string {
			if v := c.armed.Get(param); c.armed != nil && v != "" {
				return `{"data":` + v + `,"nullopt":false}`
			}
			return `{"nullopt":true}`
		}
		t := "0"
		if c.armed != nil {
			tt, _ := time.Parse(upgrades.TimeLayout, c.armed.Get("upgradetime"))
			t = strconv.FormatInt(tt.Unix(), 10)
		}
		key := `{"nullopt":true}`
		if c.armed != nil && c.armed.Get("configupgradesetkey") != "" {
			key = `{"data":"` + c.armed.Get("configupgradesetkey") + `","nullopt":false}`
		}
		return 200, fmt.Appendf(nil, `{"version":%s,"fee":%s,"reserve":%s,"configupgradesetkey":%s,"time":%s,"upgradeversion":1}`,
			opt("protocolversion"), opt("basefee"), opt("basereserve"), key, t), nil
	}
	return 404, []byte("unknown command"), nil
}

// apply is the vote passing: the ledger takes the values, the arm goes.
func (c *fakeCore) apply() {
	if v, err := strconv.ParseInt(c.armed.Get("basefee"), 10, 64); err == nil {
		c.baseFee = v
	}
	if v, err := strconv.Atoi(c.armed.Get("protocolversion")); err == nil {
		c.ledgerVersion = v
	}
	c.armed = nil
}

func (c *fakeCore) restart() { c.armed, c.startedOn = nil, "2026-11-14T15:00:00Z" }

func upgradesModel(t *testing.T) (Model, *fakeBackend) {
	return upgradesModelOn(t, "Test SDF Network ; September 2015")
}

func upgradesModelOn(t *testing.T, passphrase string) (Model, *fakeBackend) {
	t.Helper()
	cmdWait = 15 * time.Millisecond
	t.Cleanup(func() { cmdWait = 100 * time.Millisecond })
	b := newFakeBackend(testInventory("acme-a", "acme-b", "acme-c"))
	b.cores = map[string]*fakeCore{}
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		b.facts[h] = healthyFacts
		b.configs[h] = remote.RedactedConfig{PublicKey: pubA, Seed: remote.SeedPresent, NodeAlias: h,
			Text: cfgText("acme.example", passphrase)}
		b.cores[h] = newCore()
		b.cores[h].network = passphrase
	}
	m := startModel(t, b)
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		m = step(t, m, connected(h))
	}
	return m, b
}

// poll runs one info poll for every host, as the dashboard timer would.
func poll(t *testing.T, m Model) Model {
	t.Helper()
	for _, h := range []string{"acme-a", "acme-b", "acme-c"} {
		m = step(t, m, pollTickMsg{h, m.pollGen[h]})
	}
	return m
}

func TestArmBasefeeConfirmApplyAndRearm(t *testing.T) {
	m, b := upgradesModel(t)
	m = keys(t, m, "u")
	out := screen(m)
	if !strings.Contains(out, "acme-a") || !strings.Contains(out, "29/30") || !strings.Contains(out, "not armed") {
		t.Fatalf("overview:\n%s", out)
	}
	t.Run("overview", func(t *testing.T) { golden.RequireEqual(t, out) })

	// Arm: the org's hosts are preselected; the pasted string fills the form.
	m = keys(t, m, "a", "enter", "enter")
	m = step(t, m, tea.PasteMsg{Content: "upgrades?mode=set&upgradetime=2026-11-16T17:00:00Z&protocolversion=31"})
	m = keys(t, m, "enter")
	if out := screen(m); !strings.Contains(out, "2026-11-16T17:00:00Z") || !strings.Contains(out, "> 31") {
		t.Fatalf("paste did not fill the values:\n%s", out)
	}
	m = enterUntil(t, m, "Fix these first")
	out = screen(m)
	if !strings.Contains(out, "build v30.0.0 supports protocol 30 at most, not 31") || !strings.Contains(out, "Fix these first") {
		t.Fatalf("unsupported protocol not blocked:\n%s", out)
	}
	m = keys(t, m, "enter")
	for _, c := range b.cores {
		if c.armed != nil {
			t.Fatal("a blocked request was sent")
		}
	}

	// Edit: protocol out, basefee in.
	m = keys(t, m, "e", "enter", "backspace", "backspace", "enter", "200")
	m = enterUntil(t, m, "Press ⏎ to send")
	out = screen(m)
	if !strings.Contains(out, "upgrades?mode=set&upgradetime=2026-11-16T17:00:00Z&basefee=200") ||
		!strings.Contains(out, "2026-11-16 17:00:00 UTC") || !strings.Contains(out, "Press ⏎ to send") {
		t.Fatalf("review:\n%s", out)
	}
	t.Run("review", func(t *testing.T) { golden.RequireEqual(t, out) })
	m = keys(t, m, "enter")
	out = screen(m)
	if strings.Count(out, "armed and confirmed") != 3 {
		t.Fatalf("results:\n%s", out)
	}
	t.Run("results", func(t *testing.T) { golden.RequireEqual(t, out) })
	for h, c := range b.cores {
		if c.armed.Get("basefee") != "200" || c.armed.Get("upgradetime") != "2026-11-16T17:00:00Z" {
			t.Errorf("%s armed with %v", h, c.armed)
		}
	}
	if got := auditActions(b); !slices.Equal(got, []string{"upgrade.arm ok", "upgrade.arm ok", "upgrade.arm ok"}) {
		t.Errorf("audit %v", got)
	}

	// The vote passes on acme-a and acme-b: the ledger shows the new fee,
	// the overview shows it, and the change is in the audit log.
	m = keys(t, m, "esc")
	if out := screen(m); strings.Count(out, "▲ upgradetime=2026-11-") != 3 {
		t.Fatalf("armed hosts not shown:\n%s", out)
	}
	b.cores["acme-a"].apply()
	b.cores["acme-b"].apply()
	m = poll(t, m)
	out = screen(m)
	if !strings.Contains(out, "  200") || !strings.Contains(out, "the network upgrade applied") {
		t.Errorf("new base fee not shown:\n%s", out)
	}
	if strings.Count(out, "not armed") != 2 {
		t.Errorf("applied hosts still shown as armed:\n%s", out)
	}
	if got := auditActions(b); strings.Count(strings.Join(got, ","), "upgrade.applied ok") != 2 {
		t.Errorf("applied not recorded: %v", got)
	}

	// acme-c restarts before the vote and loses its schedule: re-arm is
	// offered with the same values.
	b.cores["acme-c"].restart()
	m = poll(t, m)
	out = screen(m)
	if !strings.Contains(out, "vote lost; R re-arms") || !strings.Contains(out, "no longer has the upgrade vote") || !strings.Contains(out, "R re-arm lost") {
		t.Fatalf("lost schedule not offered for re-arming:\n%s", out)
	}
	m = keys(t, m, "R")
	if out := screen(m); !strings.Contains(out, "> 200") {
		t.Fatalf("re-arm form not prefilled:\n%s", out)
	}
	m = enterUntil(t, m, "Arm 1 host(s)")
	if out := screen(m); !strings.Contains(out, "acme-c") {
		t.Fatalf("re-arm review:\n%s", out)
	}
	m = keys(t, m, "enter")
	if b.cores["acme-c"].armed.Get("basefee") != "200" || m.fleet.Host("acme-c").ArmLost != nil {
		t.Error("acme-c not re-armed")
	}
}

func TestClearUpgradesAndPubnetConfirmation(t *testing.T) {
	m, b := upgradesModel(t)
	for _, h := range []string{"acme-a", "acme-b"} {
		b.cores[h].armed = url.Values{"mode": {"set"}, "upgradetime": {"2026-11-16T17:00:00Z"}, "basefee": {"300"}}
	}
	m = poll(t, m)
	m = keys(t, m, "u")
	if out := screen(m); !strings.Contains(out, "armed differently") {
		t.Errorf("differing org not flagged:\n%s", screen(m))
	}
	m = keys(t, m, "c")
	out := screen(m)
	if !strings.Contains(out, "Clear the upgrade votes of 2 host(s)") || !strings.Contains(out, "basefee=300") || strings.Contains(out, "acme-c") {
		t.Fatalf("clear confirmation:\n%s", out)
	}
	m = keys(t, m, "enter")
	if out := screen(m); strings.Count(out, "cleared") != 2 {
		t.Fatalf("clear results:\n%s", out)
	}
	if b.cores["acme-a"].armed != nil || b.cores["acme-b"].armed != nil {
		t.Error("not cleared")
	}
	if got := auditActions(b); !slices.Equal(got, []string{"upgrade.clear ok", "upgrade.clear ok"}) {
		t.Errorf("audit %v", got)
	}
}

// enterUntil presses enter through a form until text appears.
func enterUntil(t *testing.T, m Model, text string) Model {
	t.Helper()
	for range 20 {
		if strings.Contains(screen(m), text) {
			return m
		}
		m = keys(t, m, "enter")
	}
	t.Fatalf("%q never appeared:\n%s", text, screen(m))
	return m
}

func auditActions(b *fakeBackend) []string {
	b.audit.mu.Lock()
	defer b.audit.mu.Unlock()
	var out []string
	for _, e := range b.audit.entries {
		out = append(out, e.Action+" "+e.Result)
	}
	return out
}

var proposalKey = strings.Repeat("A", 86) + "=="

func TestInspectorAndPubnetKeyArming(t *testing.T) {
	m, b := upgradesModelOn(t, "Public Global Stellar Network ; September 2015")
	rec, err := os.ReadFile("../../testdata/core/recorded/stellar-core-30.0.0/sorobaninfo_format_detailed.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.cores {
		c.soroban = rec
		c.proposals[proposalKey] = `{"updatedEntry":[{"configSettingID":"CONFIG_SETTING_CONTRACT_MAX_SIZE_BYTES","contractMaxSizeBytes":65536}]}`
	}
	m = keys(t, m, "u", "i", proposalKey, "enter", "enter")
	out := screen(m)
	if !strings.Contains(out, "1 value(s) would change") || !strings.Contains(out, "contractMaxSizeBytes: 2000 → 65536") {
		t.Fatalf("inspector:\n%s", out)
	}
	t.Run("inspector", func(t *testing.T) { golden.RequireEqual(t, out) })
	m = keys(t, m, "esc")

	// A key that does not resolve blocks arming.
	bad := strings.Repeat("Q", 86) + "=="
	m = keys(t, m, "a", "enter", "enter")
	m = step(t, m, tea.PasteMsg{Content: "upgrades?mode=set&upgradetime=2026-11-16T17:00:00Z&configupgradesetkey=" + bad})
	m = keys(t, m, "enter")
	m = enterUntil(t, m, "Fix these first")
	if out := screen(m); !strings.Contains(out, "configupgradesetkey does not resolve: ") || !strings.Contains(out, "missing or invalid") {
		t.Fatalf("unresolved key:\n%s", out)
	}
	// The real key resolves; on pubnet the operator types pubnet.
	m = keys(t, m, "esc", "a", "enter", "enter")
	m = step(t, m, tea.PasteMsg{Content: "upgrades?mode=set&upgradetime=2026-11-16T17:00:00Z&configupgradesetkey=" + proposalKey})
	m = keys(t, m, "enter")
	m = enterUntil(t, m, "Type pubnet and press ⏎ to send")
	m = keys(t, m, "enter")
	if b.cores["acme-a"].armed != nil {
		t.Fatal("sent on pubnet without the typed confirmation")
	}
	m = keys(t, m, "pubne", "enter")
	if b.cores["acme-a"].armed != nil {
		t.Fatal("sent with a wrong confirmation")
	}
	m = keys(t, m, "t", "enter")
	if out := screen(m); strings.Count(out, "armed and confirmed") != 3 {
		t.Errorf("results:\n%s", out)
	}
	for h, c := range b.cores {
		if c.armed.Get("configupgradesetkey") != proposalKey {
			t.Errorf("%s armed with %v", h, c.armed)
		}
	}
}
