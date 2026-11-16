package health

import (
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
)

var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func info(mod func(*corehttp.Info)) *corehttp.Info {
	i := &corehttp.Info{
		Build: "stellar-core 30.0.0 (abcdef)", Network: "Test SDF Network ; September 2015",
		ProtocolVersion: 30, State: "Synced!", StartedOn: "2026-11-15T08:00:00Z",
		Ledger: corehttp.LedgerInfo{Num: 1000, Age: 3, Version: 29},
		Peers:  corehttp.PeerCounts{Authenticated: 12, Pending: 2},
		Quorum: corehttp.InfoQuorum{
			QSet:       &corehttp.QSetStatus{Agree: corehttp.CountOrList{Count: 18}, FailAt: ptr(3)},
			Transitive: &corehttp.Transitive{Intersection: ptr(true)},
		},
	}
	if mod != nil {
		mod(i)
	}
	return i
}

func eval(samples ...Sample) map[string]Row { return Evaluate(samples, now, 5*time.Second) }

func TestHealthyRow(t *testing.T) {
	r := eval(Sample{Alias: "a", Info: info(nil), At: now})["a"]
	want := map[string]Cell{
		"state": {Text: "Synced!", Level: OK}, "ledger": {Text: "1000", Level: OK}, "age": {Text: "3s", Level: OK},
		"peers": {Text: "12/2", Level: OK}, "quorum": {Text: "18 f≥3", Level: OK}, "int": {Text: "yes", Level: OK},
		"proto": {Text: "30/29", Level: OK}, "build": {Text: "v30.0.0", Level: OK}, "upgrade": {Text: "—"},
		"up": {Text: "Nov 15 08:00", Level: OK},
	}
	got := map[string]Cell{"state": r.State, "ledger": r.Ledger, "age": r.Age, "peers": r.Peers, "quorum": r.Quorum,
		"int": r.Intersection, "proto": r.Protocol, "build": r.Build, "upgrade": r.Upgrade, "up": r.UpSince}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
	if r.Overall != OK || r.IntersectionLost {
		t.Errorf("overall %v lost %v", r.Overall, r.IntersectionLost)
	}
}

func TestThresholds(t *testing.T) {
	tests := []struct {
		name  string
		mod   func(*corehttp.Info)
		since time.Duration // NotSyncedSince before now
		cell  func(Row) Cell
		want  Level
	}{
		{"catching up briefly", func(i *corehttp.Info) { i.State = "Catching up" }, 30 * time.Second, func(r Row) Cell { return r.State }, Warn},
		{"catching up > 60s", func(i *corehttp.Info) { i.State = "Catching up" }, 61 * time.Second, func(r Row) Cell { return r.State }, Bad},
		{"age 9s", func(i *corehttp.Info) { i.Ledger.Age = 9 }, 0, func(r Row) Cell { return r.Age }, OK},
		{"age 10s", func(i *corehttp.Info) { i.Ledger.Age = 10 }, 0, func(r Row) Cell { return r.Age }, Bad},
		{"peers 8", func(i *corehttp.Info) { i.Peers.Authenticated = 8 }, 0, func(r Row) Cell { return r.Peers }, OK},
		{"peers 5", func(i *corehttp.Info) { i.Peers.Authenticated = 5 }, 0, func(r Row) Cell { return r.Peers }, Warn},
		{"peers 3", func(i *corehttp.Info) { i.Peers.Authenticated = 3 }, 0, func(r Row) Cell { return r.Peers }, Warn},
		{"peers 2", func(i *corehttp.Info) { i.Peers.Authenticated = 2 }, 0, func(r Row) Cell { return r.Peers }, Bad},
		{"fail_at 2", func(i *corehttp.Info) { i.Quorum.QSet.FailAt = ptr(2) }, 0, func(r Row) Cell { return r.Quorum }, OK},
		{"fail_at 1", func(i *corehttp.Info) { i.Quorum.QSet.FailAt = ptr(1) }, 0, func(r Row) Cell { return r.Quorum }, Bad},
		{"fail_at 0", func(i *corehttp.Info) { i.Quorum.QSet.FailAt = ptr(0) }, 0, func(r Row) Cell { return r.Quorum }, Bad},
		{"missing node", func(i *corehttp.Info) { i.Quorum.QSet.Missing = corehttp.CountOrList{Count: 1} }, 0, func(r Row) Cell { return r.Quorum }, Warn},
		{"no fail_at", func(i *corehttp.Info) { i.Quorum.QSet.FailAt = nil }, 0, func(r Row) Cell { return r.Quorum }, Unknown},
		{"intersection lost", func(i *corehttp.Info) { i.Quorum.Transitive.Intersection = ptr(false) }, 0, func(r Row) Cell { return r.Intersection }, Bad},
		{"intersection unknown", func(i *corehttp.Info) { i.Quorum.Transitive = nil }, 0, func(r Row) Cell { return r.Intersection }, Unknown},
		{"node behind ledger protocol", func(i *corehttp.Info) { i.ProtocolVersion = 28 }, 0, func(r Row) Cell { return r.Protocol }, Bad},
		{"protocol equal", func(i *corehttp.Info) { i.Ledger.Version = 30 }, 0, func(r Row) Cell { return r.Protocol }, OK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Sample{Alias: "a", Info: info(tt.mod), At: now}
			if tt.since > 0 {
				s.NotSyncedSince = now.Add(-tt.since)
			}
			if got := tt.cell(eval(s)["a"]); got.Level != tt.want {
				t.Errorf("level %v (%q), want %v", got.Level, got.Text, tt.want)
			}
		})
	}
}

func TestIntersectionLostRaisesBanner(t *testing.T) {
	r := eval(Sample{Alias: "a", Info: info(func(i *corehttp.Info) { i.Quorum.Transitive.Intersection = ptr(false) }), At: now})["a"]
	if !r.IntersectionLost || r.Intersection.Text != "NO" || r.Overall != Bad {
		t.Errorf("%+v", r)
	}
}

func TestLedgerLagAcrossHostsOnSameNetwork(t *testing.T) {
	rows := eval(
		Sample{Alias: "a", Info: info(func(i *corehttp.Info) { i.Ledger.Num = 1000 }), At: now},
		Sample{Alias: "b", Info: info(func(i *corehttp.Info) { i.Ledger.Num = 998 }), At: now},
		Sample{Alias: "c", Info: info(func(i *corehttp.Info) { i.Ledger.Num = 997 }), At: now},
		// Another network does not count.
		Sample{Alias: "p", Info: info(func(i *corehttp.Info) {
			i.Ledger.Num = 5_000_000
			i.Network = "Public Global Stellar Network ; September 2015"
		}), At: now},
	)
	if rows["a"].Ledger.Level != OK || rows["b"].Ledger.Level != OK || rows["c"].Ledger.Level != Bad || rows["p"].Ledger.Level != OK {
		t.Errorf("a=%v b=%v c=%v p=%v", rows["a"].Ledger.Level, rows["b"].Ledger.Level, rows["c"].Ledger.Level, rows["p"].Ledger.Level)
	}
}

func TestBuildAndArmedMismatchInOrg(t *testing.T) {
	armed := func(v string) func(*corehttp.Info) {
		return func(i *corehttp.Info) {
			i.Status = []string{"Armed with network upgrades: upgradetime=2026-11-23T17:00:00Z, " + v}
		}
	}
	rows := eval(
		Sample{Alias: "a", Org: "acme", Info: info(armed("protocolversion=30")), At: now},
		Sample{Alias: "b", Org: "acme", Info: info(armed("protocolversion=30")), At: now},
		Sample{Alias: "c", Org: "acme", Info: info(func(i *corehttp.Info) { i.Build = "stellar-core 29.1.0 (x)" }), At: now},
		Sample{Alias: "z", Org: "other", Info: info(nil), At: now},
	)
	for _, a := range []string{"a", "b", "c"} {
		if rows[a].Build.Level != Warn || rows[a].Upgrade.Level != Warn {
			t.Errorf("%s: build %v upgrade %v", a, rows[a].Build.Level, rows[a].Upgrade.Level)
		}
	}
	if rows["a"].Upgrade.Text != "P30 ▲" || rows["c"].Upgrade.Text != "none" {
		t.Errorf("upgrade texts %q %q", rows["a"].Upgrade.Text, rows["c"].Upgrade.Text)
	}
	if rows["z"].Build.Level != OK || rows["z"].Upgrade.Level != Unknown {
		t.Errorf("other org affected: %+v", rows["z"])
	}
}

func TestStaleAfterThreePolls(t *testing.T) {
	fresh := eval(Sample{Alias: "a", Info: info(nil), At: now.Add(-15 * time.Second)})["a"]
	stale := eval(Sample{Alias: "a", Info: info(nil), At: now.Add(-16 * time.Second)})["a"]
	if fresh.Ledger.Stale || !stale.Ledger.Stale || stale.Ledger.Text != "1000" {
		t.Errorf("fresh %+v stale %+v", fresh.Ledger, stale.Ledger)
	}
}

func TestNoInfoYet(t *testing.T) {
	r := eval(Sample{Alias: "a"})["a"]
	if r.State.Text != "—" || r.Overall != Unknown {
		t.Errorf("%+v", r)
	}
}

func TestUpgradeLabel(t *testing.T) {
	for in, want := range map[string]string{
		"Armed with network upgrades: upgradetime=2026-11-23T17:00:00Z, protocolversion=30": "P30",
		"Armed with network upgrades: upgradetime=2026-11-23T17:00:00Z, basefee=200":        "basefee",
		"Armed with network upgrades: something":                                            "armed",
	} {
		if got := UpgradeLabel(in); got != want {
			t.Errorf("UpgradeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
