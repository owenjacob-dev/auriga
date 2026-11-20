package corehttp

import (
	"encoding/json"
	"testing"
)

// TestPendingPeersAreAddresses decodes pending peers the way stellar-core
// 30.0.0 sends them: bare address strings, while authenticated peers are
// objects. Objects are still accepted in the pending lists.
func TestPendingPeersAreAddresses(t *testing.T) {
	body := []byte(`{
		"authenticated_peers": {"inbound": [{"address": "10.0.0.2:52110", "id": "itest-c", "latency": 1, "elapsed": 92, "olver": 43, "ver": "stellar-core 30.0.0"}], "outbound": null},
		"pending_peers": {"inbound": ["198.51.100.7:40122"], "outbound": ["203.0.113.9:11625", {"address": "192.0.2.1:11625"}]}
	}`)
	var r PeersResponse
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if got := r.Authenticated.Inbound; len(got) != 1 || got[0].ID != "itest-c" || got[0].Latency != 1 || got[0].Address != "10.0.0.2:52110" {
		t.Errorf("authenticated = %+v", got)
	}
	if got := r.Pending.Inbound; len(got) != 1 || got[0] != (Peer{Address: "198.51.100.7:40122"}) {
		t.Errorf("pending inbound = %+v", got)
	}
	if got := r.Pending.Outbound; len(got) != 2 || got[0].Address != "203.0.113.9:11625" || got[1].Address != "192.0.2.1:11625" {
		t.Errorf("pending outbound = %+v", got)
	}
	var p Peer
	if err := json.Unmarshal([]byte(`42`), &p); err == nil {
		t.Error("a number decoded as a peer")
	}
}
