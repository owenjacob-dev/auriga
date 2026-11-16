package corehttp

import (
	"context"
	"testing"
)

// Responses recorded from stellar-core 30.0.0 on the integration network
// (testdata/core/recorded). These pin the field names to a real build.
const rec30 = "recorded/stellar-core-30.0.0/"

func recordedClient(t *testing.T) *Client {
	t.Helper()
	return New(&canned{bodies: map[string][]byte{
		"/info":                                 fixture(t, rec30+"info.json"),
		"/quorum?compact=false":                 fixture(t, rec30+"quorum_compact_false.json"),
		"/quorum?compact=false&transitive=true": fixture(t, rec30+"quorum_compact_false_transitive_true.json"),
		"/peers":                                fixture(t, rec30+"peers.json"),
		"/upgrades?mode=get":                    fixture(t, rec30+"upgrades_mode_get.json"),
	}})
}

func TestRecorded30Info(t *testing.T) {
	r, _, err := recordedClient(t).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := r.Info
	if i.State != "Synced!" || i.Network != "Auriga Integration Network ; November 2026" || i.ProtocolVersion != 30 ||
		i.Ledger.Num == 0 || i.Ledger.Version != 0 || i.Peers.Authenticated != 2 {
		t.Errorf("info %+v", i)
	}
	q := i.Quorum
	if q.Node != "itest-a" || q.QSet == nil || q.QSet.Agree.N() != 3 || q.QSet.FailAt == nil || *q.QSet.FailAt != 2 ||
		q.Transitive == nil || q.Transitive.Intersection == nil || !*q.Transitive.Intersection || q.Transitive.NodeCount != 3 {
		t.Errorf("quorum %+v qset %+v transitive %+v", q, q.QSet, q.Transitive)
	}
	if i.Armed() != "" {
		t.Errorf("armed %q on an unarmed node", i.Armed())
	}
}

func TestRecorded30Quorum(t *testing.T) {
	c := recordedClient(t)
	q, _, err := c.Quorum(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	qs := q.QSet
	if qs == nil || qs.Agree.N() != 3 || len(qs.Agree.List) != 3 || qs.Missing.N() != 0 || qs.Disagree.N() != 0 ||
		qs.Delayed.N() != 0 || *qs.FailAt != 2 || len(qs.FailWith) != 2 || qs.Value == nil || qs.Value.Threshold != 2 || len(qs.Value.Nodes) != 3 {
		t.Errorf("qset %+v value %+v", qs, qs.Value)
	}
	tq, _, err := c.Quorum(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if tq.Transitive == nil || tq.Transitive.Intersection == nil || !*tq.Transitive.Intersection || tq.Transitive.NodeCount != 3 {
		t.Errorf("transitive %+v", tq.Transitive)
	}
	if len(tq.Nodes) != 3 || tq.Nodes[0].Status != "tracking" || tq.Nodes[0].QSet == nil || tq.Nodes[0].QSet.Threshold != 2 {
		t.Errorf("nodes %+v", tq.Nodes)
	}
}

func TestRecorded30Peers(t *testing.T) {
	p, _, err := recordedClient(t).Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	all := append(p.Authenticated.Inbound, p.Authenticated.Outbound...)
	if len(all) != 2 {
		t.Fatalf("peers %+v", p)
	}
	for _, pr := range all {
		if pr.ID == "" || pr.Address == "" || pr.Version == "" || pr.OLVer == 0 {
			t.Errorf("peer %+v", pr)
		}
	}
}

func TestRecorded30UpgradesNothingScheduled(t *testing.T) {
	u, _, err := recordedClient(t).Upgrades(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*u) < 5 {
		t.Fatalf("expected every field, got %v", *u)
	}
	if s := u.Scheduled(); len(s) != 0 {
		t.Errorf("Scheduled() = %v, want nothing", s)
	}
	set := UpgradesResponse{"fee": []byte(`200`), "time": []byte(`1795000000`), "version": []byte(`{"nullopt":true}`), "upgradeversion": []byte(`1`)}
	if s := set.Scheduled(); len(s) != 2 || s["fee"] != "200" || s["time"] != "1795000000" {
		t.Errorf("Scheduled() = %v", s)
	}
}
