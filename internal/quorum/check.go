package quorum

import (
	"fmt"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// Severity of a quorum issue.
type Severity int

const (
	// Warn is shown with the set.
	Warn Severity = iota
	// Block stops the wizard until fixed.
	Block
	// NeedsConsent blocks until the operator accepts it explicitly.
	NeedsConsent
)

// Issue is one quorum finding.
type Issue struct {
	Rule     string
	Entry    string
	Message  string
	Severity Severity
}

// MinTolerance is the fault tolerance stellar-core 30 requires at the top
// of the quorum set without UNSAFE_QUORUM (docs/decisions.md, M4).
const MinTolerance = 1

// CheckOptions carry what the checks compare against.
type CheckOptions struct {
	// Running is the config currently on the host, if any, to list
	// validators the new set removes.
	Running *stellarcfg.Config
}

// Check runs the quorum-wide rules on one node's config (SPEC.md "Quorum
// checks"). Per-entry rules (StrKey, duplicate names and keys, listing the
// node itself) are in stellarcfg.Validate.
func Check(cfg *stellarcfg.Config, self Self, opt CheckOptions) []Issue {
	var out []Issue
	add := func(sev Severity, rule, entry, format string, args ...any) {
		out = append(out, Issue{Rule: rule, Entry: entry, Message: fmt.Sprintf(format, args...), Severity: sev})
	}
	if self.PublicKey != "" {
		for _, v := range cfg.Validators {
			if v.PublicKey == self.PublicKey {
				add(Block, "self-listed", v.Name, "is this node's own key; list siblings only (stellar-core refuses: naming node twice)")
			}
		}
	}
	nodes, unrated := Resolve(cfg, self)
	for _, n := range unrated {
		who := n.Name
		if n.Self {
			who += " (this node)"
		}
		add(Block, "no-quality", who, "has no QUALITY of its own and its home domain %q has none", n.HomeDomain)
	}

	// HIGH and CRITICAL orgs need three validators (the node itself counts
	// for its own org) and an archive for every listed validator.
	count := map[string]int{}
	quality := map[string]stellarcfg.Quality{}
	for _, n := range nodes {
		count[n.HomeDomain]++
		if q := quality[n.HomeDomain]; q == "" || n.Quality.Valid() && rank(n.Quality) < rank(q) {
			quality[n.HomeDomain] = n.Quality
		}
	}
	for d, q := range quality {
		if (q == stellarcfg.High || q == stellarcfg.Critical) && count[d] < 3 {
			add(Block, "redundancy", d, "is %s but has %d validators; %s orgs need at least 3 (this node counts for its own org)", q, count[d], q)
		}
		if q == stellarcfg.Critical {
			add(Warn, "critical", d, "is CRITICAL: if it cannot agree, this node stops closing ledgers (safety over liveness)")
		}
	}
	for _, n := range nodes {
		if !n.Self && (n.Quality == stellarcfg.High || n.Quality == stellarcfg.Critical) && n.History == "" {
			add(Block, "history-required", n.Name, "is %s and needs a HISTORY archive", n.Quality)
		}
	}

	if set := Generate(cfg, self); set != nil {
		if set.Size() < 4 {
			add(Warn, "few-entities", "", "only %d top-level entities; SDF recommends at least four", set.Size())
		}
		if set.Tolerance() < MinTolerance {
			sev := NeedsConsent
			if cfg.UnsafeQuorum != nil && *cfg.UnsafeQuorum {
				sev = Warn
			}
			add(sev, "zero-tolerance", "",
				"tolerates no failures (%d of %d at the top); stellar-core needs UNSAFE_QUORUM=true", set.Threshold, set.Size())
		}
		// stellar-core's verifyHistoryValidatorsBlocking: the validators that
		// publish an archive must be v-blocking, so no quorum can form
		// without one of them (the node itself is not counted).
		if !set.VBlocking(func(n Node) bool { return !n.Self && n.History != "" }) {
			sev := NeedsConsent
			if cfg.UnsafeQuorum != nil && *cfg.UnsafeQuorum {
				sev = Warn
			}
			add(sev, "archives-blocking", "",
				"can reach quorum without any validator that has a HISTORY archive; stellar-core needs UNSAFE_QUORUM=true")
		}
	}

	if opt.Running != nil {
		keep := map[string]bool{}
		for _, v := range cfg.Validators {
			keep[v.PublicKey] = true
		}
		for _, v := range opt.Running.Validators {
			if !keep[v.PublicKey] {
				add(Warn, "removed", v.Name, "is in the running config but not in the new one; coordinate with other validators before removing it")
			}
		}
	}
	return out
}

func rank(q stellarcfg.Quality) int {
	for i, l := range levels {
		if l == q {
			return i
		}
	}
	return len(levels)
}

// Blocking reports whether any issue stops the wizard. Consent issues block
// until accepted (consented is true).
func Blocking(issues []Issue, consented bool) bool {
	for _, i := range issues {
		if i.Severity == Block || (i.Severity == NeedsConsent && !consented) {
			return true
		}
	}
	return false
}
