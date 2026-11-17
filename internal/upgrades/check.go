package upgrades

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Host is what the checks need to know about one target host.
type Host struct {
	Alias, Org string
	Network    string // "testnet", "pubnet", or "" when unknown
	Build      string
	// ProtocolVersion is the highest protocol the build supports;
	// LedgerVersion is the current ledger's.
	ProtocolVersion, LedgerVersion int
	// Armed is the info status line, "" when not armed.
	Armed string
}

// Issue is a problem found before sending.
type Issue struct {
	Host    string // "" for the request as a whole
	Rule    string
	Message string
	Block   bool
}

func (i Issue) String() string {
	if i.Host == "" {
		return i.Message
	}
	return i.Host + ": " + i.Message
}

// MaxAhead is how far ahead an upgradetime may be without a warning.
const MaxAhead = 30 * 24 * time.Hour

// Check runs SPEC.md "Checks before sending" for p on hosts, the network
// chosen on the form, at now. Resolving a configupgradesetkey with
// dumpproposedsettings needs a host, so the caller adds that result.
func Check(p Params, network string, hosts []Host, now time.Time) []Issue {
	var out []Issue
	block := func(host, rule, format string, args ...any) {
		out = append(out, Issue{Host: host, Rule: rule, Message: fmt.Sprintf(format, args...), Block: true})
	}
	warn := func(host, rule, format string, args ...any) {
		out = append(out, Issue{Host: host, Rule: rule, Message: fmt.Sprintf(format, args...)})
	}
	if len(hosts) == 0 {
		block("", "no-hosts", "choose at least one host")
	}
	switch {
	case p.Time.IsZero():
		block("", "time", "upgradetime is required")
	case p.Time.Before(now):
		warn("", "time-past", "upgradetime %s is in the past; it may apply at once or be dropped, depending on the build's expiry window", p.Time.UTC().Format(TimeLayout))
	case p.Time.After(now.Add(MaxAhead)):
		warn("", "time-far", "upgradetime is more than 30 days ahead")
	}
	if p.Settings() == 0 {
		block("", "nothing", "set at least one value besides upgradetime")
	}
	if p.ConfigUpgradeSetKey != "" {
		if err := CheckKey(p.ConfigUpgradeSetKey); err != nil {
			block("", "key", "%v", err)
		}
	}
	proto, hasProto := p.Get("protocolversion")
	builds := map[string]bool{}
	for _, h := range hosts {
		if network != "" && h.Network != network {
			got := h.Network
			if got == "" {
				got = "an unknown network"
			}
			block(h.Alias, "network", "is on %s, not %s", got, network)
		}
		if hasProto {
			switch {
			case h.ProtocolVersion == 0:
				block(h.Alias, "protocol-unknown", "has not reported the protocol its build supports yet")
			case int(proto) > h.ProtocolVersion:
				block(h.Alias, "protocol-unsupported", "build %s supports protocol %d at most, not %d", h.Build, h.ProtocolVersion, proto)
			case int(proto) <= h.LedgerVersion:
				block(h.Alias, "protocol-not-higher", "the ledger is already at protocol %d; %d is not an upgrade", h.LedgerVersion, proto)
			}
		}
		if h.Build != "" {
			builds[h.Build] = true
		}
	}
	if len(builds) > 1 {
		var list []string
		for b := range builds {
			list = append(list, b)
		}
		sort.Strings(list)
		warn("", "builds-differ", "the hosts run different builds: %s", strings.Join(list, ", "))
	}
	for _, org := range ArmedDifferently(hosts) {
		warn("", "armed-differently", "hosts in %s are armed differently now", org)
	}
	return out
}

// ArmedDifferently lists orgs whose hosts report different armed status.
func ArmedDifferently(hosts []Host) []string {
	seen := map[string]map[string]bool{}
	for _, h := range hosts {
		if h.Org == "" {
			continue
		}
		if seen[h.Org] == nil {
			seen[h.Org] = map[string]bool{}
		}
		seen[h.Org][h.Armed] = true
	}
	var out []string
	for org, states := range seen {
		if len(states) > 1 {
			out = append(out, org)
		}
	}
	sort.Strings(out)
	return out
}

// Blocking reports whether any issue stops the request.
func Blocking(issues []Issue) bool {
	for _, i := range issues {
		if i.Block {
			return true
		}
	}
	return false
}
