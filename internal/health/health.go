// Package health turns polled `info` responses into the dashboard's cells
// and colours, using the thresholds in SPEC.md "Dashboard" (taken from SDF's
// monitoring guide). It is pure: no I/O, no UI types.
package health

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
)

// Level is a cell's health. The UI pairs each with a symbol and a word.
type Level int

const (
	Unknown Level = iota
	OK
	Warn
	Bad
)

// Thresholds from SPEC.md.
const (
	SyncedState        = "Synced!"
	NotSyncedBadAfter  = 60 * time.Second
	LedgerSlack        = 2
	AgeBadSeconds      = 10
	PeersOK            = 8
	PeersBad           = 3
	FailAtOK           = 2
	StaleAfterPolls    = 3
	FailuresBeforeSlow = 3
	SlowRetry          = 30 * time.Second
)

// Sample is what the poller knows about one host.
type Sample struct {
	Alias   string
	Org     string
	Network string // from the inventory, "" if unknown
	// Info is the last successful info; nil if never fetched.
	Info *corehttp.Info
	// At is when Info was fetched.
	At time.Time
	// NotSyncedSince is when the state last left "Synced!" (zero if synced
	// or unknown).
	NotSyncedSince time.Time
}

// Cell is one dashboard cell.
type Cell struct {
	Text  string
	Level Level
	Stale bool
}

// Row holds the info-derived cells for one host.
type Row struct {
	State, Ledger, Age, Peers, Quorum, Intersection, Protocol, Build, Upgrade, UpSince Cell
	// IntersectionLost is true when the node reports no quorum intersection
	// (raises the red banner).
	IntersectionLost bool
	// Overall is the worst level among the cells.
	Overall Level
}

var dash = Cell{Text: "—"}

// Evaluate computes rows for every sample. Cross-host checks (ledger lag,
// build and upgrade mismatches) compare hosts on the same network or in the
// same org. interval is the poll interval, for staleness.
func Evaluate(samples []Sample, now time.Time, interval time.Duration) map[string]Row {
	maxLedger := map[string]uint64{}
	builds := map[string]map[string]bool{}
	armed := map[string]map[string]bool{}
	for _, s := range samples {
		if s.Info == nil {
			continue
		}
		net := s.network()
		if s.Info.Ledger.Num > maxLedger[net] {
			maxLedger[net] = s.Info.Ledger.Num
		}
		if s.Org != "" {
			if builds[s.Org] == nil {
				builds[s.Org], armed[s.Org] = map[string]bool{}, map[string]bool{}
			}
			builds[s.Org][s.Info.Build] = true
			armed[s.Org][armedValues(s.Info.Armed())] = true
		}
	}
	out := make(map[string]Row, len(samples))
	for _, s := range samples {
		r := Row{State: dash, Ledger: dash, Age: dash, Peers: dash, Quorum: dash, Intersection: dash,
			Protocol: dash, Build: dash, Upgrade: dash, UpSince: dash}
		if s.Info == nil {
			out[s.Alias] = r
			continue
		}
		i := s.Info
		stale := interval > 0 && now.Sub(s.At) > StaleAfterPolls*interval

		r.State = Cell{Text: i.State, Level: OK}
		if i.State != SyncedState {
			r.State.Level = Warn
			if !s.NotSyncedSince.IsZero() && now.Sub(s.NotSyncedSince) > NotSyncedBadAfter {
				r.State.Level = Bad
			}
		}

		r.Ledger = Cell{Text: strconv.FormatUint(i.Ledger.Num, 10), Level: OK}
		if maxLedger[s.network()]-i.Ledger.Num > LedgerSlack {
			r.Ledger.Level = Bad
		}

		r.Age = Cell{Text: fmt.Sprintf("%ds", i.Ledger.Age), Level: OK}
		if i.Ledger.Age >= AgeBadSeconds {
			r.Age.Level = Bad
		}

		r.Peers = Cell{Text: fmt.Sprintf("%d/%d", i.Peers.Authenticated, i.Peers.Pending), Level: Warn}
		switch {
		case i.Peers.Authenticated >= PeersOK:
			r.Peers.Level = OK
		case i.Peers.Authenticated < PeersBad:
			r.Peers.Level = Bad
		}

		if q := i.Quorum.QSet; q != nil {
			text := strconv.Itoa(q.Agree.N())
			r.Quorum = Cell{Text: text, Level: Unknown}
			if q.FailAt != nil {
				r.Quorum.Text = fmt.Sprintf("%s f≥%d", text, *q.FailAt)
				r.Quorum.Level = OK
				if *q.FailAt < FailAtOK {
					r.Quorum.Level = Bad
				}
			}
			if m := q.Missing.N(); m > 0 {
				r.Quorum.Text += fmt.Sprintf(" -%d", m)
				if r.Quorum.Level == OK {
					r.Quorum.Level = Warn
				}
			}
		}
		if t := i.Quorum.Transitive; t != nil && t.Intersection != nil {
			if *t.Intersection {
				r.Intersection = Cell{Text: "yes", Level: OK}
			} else {
				r.Intersection = Cell{Text: "NO", Level: Bad}
				r.IntersectionLost = true
			}
		}

		r.Protocol = Cell{Text: fmt.Sprintf("%d/%d", i.ProtocolVersion, i.Ledger.Version), Level: OK}
		if i.ProtocolVersion < i.Ledger.Version {
			r.Protocol.Level = Bad
		}

		r.Build = Cell{Text: shortBuild(i.Build), Level: OK}
		if s.Org != "" && len(builds[s.Org]) > 1 {
			r.Build.Level = Warn
		}

		if a := i.Armed(); a != "" {
			r.Upgrade = Cell{Text: UpgradeLabel(a) + " ▲", Level: OK}
		}
		if s.Org != "" && len(armed[s.Org]) > 1 {
			if r.Upgrade.Text == dash.Text {
				r.Upgrade.Text = "none"
			}
			r.Upgrade.Level = Warn
		}

		if t, err := time.Parse(time.RFC3339, i.StartedOn); err == nil {
			r.UpSince = Cell{Text: t.UTC().Format("Jan 02 15:04"), Level: OK}
		}

		if stale {
			for _, c := range []*Cell{&r.State, &r.Ledger, &r.Age, &r.Peers, &r.Quorum, &r.Intersection, &r.Protocol, &r.Build, &r.Upgrade, &r.UpSince} {
				c.Stale = true
			}
		}
		for _, c := range []Cell{r.State, r.Ledger, r.Age, r.Peers, r.Quorum, r.Intersection, r.Protocol, r.Build, r.Upgrade} {
			if c.Level > r.Overall {
				r.Overall = c.Level
			}
		}
		out[s.Alias] = r
	}
	return out
}

func (s Sample) network() string {
	if s.Info != nil && s.Info.Network != "" {
		return s.Info.Network
	}
	return s.Network
}

// armedValues is the comparable part of an "Armed with network upgrades"
// line (empty when not armed).
func armedValues(line string) string {
	_, v, _ := strings.Cut(line, ":")
	return strings.TrimSpace(v)
}

var protoRE = regexp.MustCompile(`protocolversion=(\d+)`)

// UpgradeLabel shortens an armed status line: "P30" for a protocol upgrade,
// otherwise the first upgraded setting.
func UpgradeLabel(line string) string {
	v := armedValues(line)
	if m := protoRE.FindStringSubmatch(v); m != nil {
		return "P" + m[1]
	}
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		k, _, ok := strings.Cut(part, "=")
		if ok && k != "upgradetime" && k != "time" {
			return k
		}
	}
	return "armed"
}

var buildRE = regexp.MustCompile(`v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?`)

// shortBuild extracts "v30.0.0" from a build string.
func shortBuild(b string) string {
	if m := buildRE.FindString(b); m != "" {
		if !strings.HasPrefix(m, "v") {
			m = "v" + m
		}
		return m
	}
	return b
}
