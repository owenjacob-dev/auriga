package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
)

func coreFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "core", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// healthyFacts is a probe result with nothing to warn about.
var healthyFacts = remote.ProbeFacts{
	CoreInstalled: true, CoreVersion: "stellar-core 30.0.0", ServiceActive: "active", ServiceEnabled: "enabled",
	ServiceLoad: "loaded", ConfigPresent: true, ConfigReadable: true, ConfigMode: "640", HTTPPort: 11626,
	DiskAvailKB: 120 << 20, DiskTotalKB: 200 << 20, NTPSync: "yes",
}

func TestDashboardShowsPolledInfo(t *testing.T) {
	b := newFakeBackend(testInventory("acme-a", "acme-b", "acme-c"))
	for _, a := range []string{"acme-a", "acme-b", "acme-c"} {
		b.facts[a] = healthyFacts
	}
	b.admin["acme-a"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-pubnet-synced.json")}
	b.admin["acme-b"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-catching-up.json")}
	m := startModel(t, b)
	m = step(t, m, connected("acme-a"))
	m = step(t, m, connected("acme-b"))
	m = step(t, m, poolEventMsg{sshx.StateEvent{HostState: sshx.HostState{Alias: "acme-c", State: sshx.StateDisconnected,
		Err: "cannot reach acme-c at 10.0.0.3:22 (timeout 15s): i/o timeout"}}})
	out := ansi.Strip(m.Render())
	for _, want := range []string{"Synced!", "61234567", "12/2", "18 f≥3", "yes", "30/29", "P30 ▲", "Catching up", "61234101", "disconnected"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	golden.RequireEqual(t, out)
	// acme-b's problems are spelled out in its status line.
	m = step(t, m, keyPress("j"))
	if out := ansi.Strip(m.Render()); !strings.Contains(out, "466 ledgers behind") || !strings.Contains(out, "last ledger closed 41s ago") {
		t.Errorf("status for acme-b:\n%s", out)
	}
}

func TestIntersectionLostRaisesBanner(t *testing.T) {
	b := newFakeBackend(testInventory("acme-a", "acme-b"))
	b.facts["acme-a"], b.facts["acme-b"] = healthyFacts, healthyFacts
	b.admin["acme-a"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-pubnet-synced.json")}
	b.admin["acme-b"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-intersection-lost.json")}
	m := startModel(t, b)
	m = step(t, m, connected("acme-a"))
	m = step(t, m, connected("acme-b"))
	out := ansi.Strip(m.Render())
	if !strings.Contains(out, "NO QUORUM INTERSECTION (acme-b)") {
		t.Fatalf("no banner:\n%s", out)
	}
	golden.RequireEqual(t, out)
}

func TestPollFailuresSlowDownAndRecover(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	b.adminErr["a"] = errors.New("connection refused")
	m := startModel(t, b)
	m = step(t, m, connected("a"))
	gen := m.pollGen["a"]
	for range 2 {
		m = step(t, m, pollTickMsg{"a", gen})
	}
	hv := m.fleet.Host("a")
	if hv.PollFailures != 3 || !strings.Contains(hv.InfoErr, "connection refused") {
		t.Fatalf("failures %d err %q", hv.PollFailures, hv.InfoErr)
	}
	if d := m.pollDelay(hv.PollFailures); d != 30*time.Second {
		t.Errorf("delay after 3 failures %v, want 30s", d)
	}
	if d := m.pollDelay(2); d != 5*time.Second {
		t.Errorf("delay after 2 failures %v, want 5s", d)
	}
	b.adminErr["a"] = nil
	b.admin["a"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-pubnet-synced.json")}
	m = step(t, m, pollTickMsg{"a", gen})
	if hv := m.fleet.Host("a"); hv.PollFailures != 0 || hv.InfoErr != "" || hv.Info == nil {
		t.Errorf("not recovered: %+v", hv)
	}
}

func TestPauseAndStaleTimers(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	b.admin["a"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-pubnet-synced.json")}
	m := startModel(t, b)
	m = step(t, m, connected("a"))
	calls := len(b.adminCalls)
	gen := m.pollGen["a"]

	m = step(t, m, keyPress("p"))
	if !m.fleet.Paused || !strings.Contains(ansi.Strip(m.Render()), "polling paused") {
		t.Fatal("not paused")
	}
	m = step(t, m, pollTickMsg{"a", gen})
	if len(b.adminCalls) != calls {
		t.Error("polled while paused")
	}
	m = step(t, m, keyPress("p"))
	m = step(t, m, pollTickMsg{"a", gen - 1}) // a timer from an earlier connection
	if len(b.adminCalls) != calls {
		t.Error("stale timer polled")
	}
	m = step(t, m, pollTickMsg{"a", gen})
	if len(b.adminCalls) != calls+1 {
		t.Errorf("calls %d, want %d", len(b.adminCalls), calls+1)
	}
}

func TestNoPollingWhileDisconnected(t *testing.T) {
	b := newFakeBackend(testInventory("a"))
	b.admin["a"] = map[string][]byte{"info": coreFixture(t, "synthetic/info-pubnet-synced.json")}
	m := startModel(t, b)
	m = step(t, m, connected("a"))
	gen, calls := m.pollGen["a"], len(b.adminCalls)
	m = step(t, m, poolEventMsg{sshx.StateEvent{HostState: sshx.HostState{Alias: "a", State: sshx.StateDisconnected}}})
	step(t, m, pollTickMsg{"a", gen})
	if len(b.adminCalls) != calls {
		t.Error("polled a disconnected host")
	}
}
