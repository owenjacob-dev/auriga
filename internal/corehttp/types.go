package corehttp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// InfoResponse is the body of `info`.
type InfoResponse struct {
	Info Info `json:"info"`
}

// Info is the node summary. Fields missing in older builds stay zero.
type Info struct {
	Build           string     `json:"build"`
	Network         string     `json:"network"`
	ProtocolVersion int        `json:"protocol_version"`
	State           string     `json:"state"`
	StartedOn       string     `json:"startedOn"`
	Ledger          LedgerInfo `json:"ledger"`
	Peers           PeerCounts `json:"peers"`
	Quorum          InfoQuorum `json:"quorum"`
	Status          []string   `json:"status"`
	HistoryFailures string     `json:"history_failure_rate"`
}

// LedgerInfo is info.ledger.
type LedgerInfo struct {
	Num                 uint64 `json:"num"`
	Age                 int64  `json:"age"`
	Version             int    `json:"version"`
	BaseFee             int64  `json:"baseFee"`
	BaseReserve         int64  `json:"baseReserve"`
	MaxTxSetSize        int64  `json:"maxTxSetSize"`
	MaxSorobanTxSetSize int64  `json:"maxSorobanTxSetSize"`
	CloseTime           int64  `json:"closeTime"`
	Hash                string `json:"hash"`
}

// PeerCounts is info.peers.
type PeerCounts struct {
	Authenticated int `json:"authenticated_count"`
	Pending       int `json:"pending_count"`
}

// InfoQuorum is info.quorum. Current builds put qset and transitive
// directly under quorum; older builds keyed the qset by ledger number.
type InfoQuorum struct {
	Node       string      `json:"node"`
	QSet       *QSetStatus `json:"qset"`
	Transitive *Transitive `json:"transitive"`
}

// UnmarshalJSON accepts both layouts.
func (q *InfoQuorum) UnmarshalJSON(b []byte) error {
	type plain InfoQuorum
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.QSet == nil {
		// Legacy: {"<ledger>": {qset fields}, "transitive": {...}}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err == nil {
			var best uint64
			for k, v := range m {
				n, err := strconv.ParseUint(k, 10, 64)
				if err != nil || n < best {
					continue
				}
				var qs QSetStatus
				if json.Unmarshal(v, &qs) == nil {
					best, p.QSet = n, &qs
				}
			}
		}
	}
	*q = InfoQuorum(p)
	return nil
}

// QSetStatus is the state of the node's own quorum set at a ledger.
type QSetStatus struct {
	Agree     CountOrList `json:"agree"`
	Delayed   CountOrList `json:"delayed"`
	Disagree  CountOrList `json:"disagree"`
	Missing   CountOrList `json:"missing"`
	FailAt    *int        `json:"fail_at"`
	FailWith  []string    `json:"fail_with"`
	Hash      string      `json:"hash"`
	Ledger    uint64      `json:"ledger"`
	Phase     string      `json:"phase"`
	Validated *bool       `json:"validated"`
	Value     *QSetValue  `json:"value"`
}

// QSetValue is the configured quorum set: threshold and members, where a
// member is a node ID or a nested set.
type QSetValue struct {
	Threshold int          `json:"t"`
	Nodes     []string     `json:"-"`
	Inner     []*QSetValue `json:"-"`
}

// UnmarshalJSON splits "v" into node IDs and nested sets.
func (v *QSetValue) UnmarshalJSON(b []byte) error {
	var raw struct {
		T int               `json:"t"`
		V []json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	v.Threshold = raw.T
	for _, m := range raw.V {
		m = bytes.TrimSpace(m)
		if len(m) > 0 && m[0] == '"' {
			var s string
			if err := json.Unmarshal(m, &s); err == nil {
				v.Nodes = append(v.Nodes, s)
			}
			continue
		}
		var inner QSetValue
		if err := json.Unmarshal(m, &inner); err == nil {
			v.Inner = append(v.Inner, &inner)
		}
	}
	return nil
}

// Transitive is the network-wide quorum intersection summary.
type Transitive struct {
	Intersection    *bool      `json:"intersection"`
	Critical        [][]string `json:"critical"`
	LastCheckLedger uint64     `json:"last_check_ledger"`
	NodeCount       int        `json:"node_count"`
	PotentialSplit  [][]string `json:"potential_split"`
}

// CountOrList is a field that compact output gives as a number and full
// output as a list of node IDs.
type CountOrList struct {
	Count int
	List  []string
}

// N is the count, from whichever form was sent.
func (c CountOrList) N() int {
	if c.List != nil {
		return len(c.List)
	}
	return c.Count
}

// UnmarshalJSON accepts a number, a list of strings, or null.
func (c *CountOrList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		return nil
	case b[0] == '[':
		c.List = []string{}
		return json.Unmarshal(b, &c.List)
	default:
		return json.Unmarshal(b, &c.Count)
	}
}

// MarshalJSON writes the list when present, otherwise the count.
func (c CountOrList) MarshalJSON() ([]byte, error) {
	if c.List != nil {
		return json.Marshal(c.List)
	}
	return json.Marshal(c.Count)
}

// QuorumResponse is the body of `quorum?compact=false`.
type QuorumResponse struct {
	Node       string      `json:"node"`
	QSet       *QSetStatus `json:"qset"`
	Transitive *Transitive `json:"transitive"`
}

// PeersResponse is the body of `peers`.
type PeersResponse struct {
	Authenticated PeerDirections `json:"authenticated_peers"`
	Pending       PeerDirections `json:"pending_peers"`
}

// PeerDirections splits peers by who opened the connection.
type PeerDirections struct {
	Inbound  []Peer `json:"inbound"`
	Outbound []Peer `json:"outbound"`
}

// Peer is one overlay connection.
type Peer struct {
	Address string `json:"address"`
	ID      string `json:"id"`
	Version string `json:"ver"`
	Latency int64  `json:"latency"` // milliseconds
	Elapsed int64  `json:"elapsed"` // seconds connected
	OLVer   int    `json:"olver"`
}

// UpgradesResponse is the body of `upgrades?mode=get`: the scheduled
// parameters. Field names vary by build, so the values are kept as a map.
type UpgradesResponse map[string]json.RawMessage

// Armed returns the "Armed with network upgrades: …" status line, if any
// (SPEC.md: that is how info reports an armed node).
func (i Info) Armed() string {
	for _, s := range i.Status {
		if strings.HasPrefix(s, "Armed with network upgrades") {
			return s
		}
	}
	return ""
}
