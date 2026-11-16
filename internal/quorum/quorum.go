// Package quorum previews and checks the quorum set stellar-core generates
// from [[HOME_DOMAINS]] and [[VALIDATORS]] (SPEC.md "Quorum sets", "Step 5:
// quorum set builder").
//
// stellar-core builds the quorum set itself; this package mirrors its
// algorithm so the wizard can warn before a deploy. The result is a preview:
// after a deploy the node's quorum endpoint is authoritative. The generation
// and the safety rule are checked against quorum sets stellar-core 30.0.0
// produced (testdata/core30).
package quorum

import (
	"fmt"
	"sort"
	"strings"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// Node is one validator in the generated set.
type Node struct {
	Name       string
	PublicKey  string
	HomeDomain string
	Quality    stellarcfg.Quality
	History    string
	Self       bool // this node, which core adds to its own home domain
}

// Set is a quorum set: a threshold over validators and inner sets.
type Set struct {
	Threshold int
	Nodes     []Node
	Inner     []*Set
	// Label says what the set stands for: a home domain, or a quality level.
	Label string
}

// VBlocking reports whether the nodes in for form a v-blocking set: one
// that every quorum slice intersects, as stellar-core's
// LocalNode::isVBlocking computes it.
func (s *Set) VBlocking(in func(Node) bool) bool {
	if s == nil || s.Threshold == 0 {
		return false
	}
	left := 1 + s.Size() - s.Threshold
	for _, n := range s.Nodes {
		if in(n) {
			if left--; left <= 0 {
				return true
			}
		}
	}
	for _, inner := range s.Inner {
		if inner.VBlocking(in) {
			if left--; left <= 0 {
				return true
			}
		}
	}
	return false
}

// Size is the number of members (validators and inner sets).
func (s *Set) Size() int { return len(s.Nodes) + len(s.Inner) }

// Tolerance is how many members can fail before the set loses its
// threshold.
func (s *Set) Tolerance() int { return s.Size() - s.Threshold }

// Self identifies the node the config is for.
type Self struct {
	Name       string // NODE_SEED alias
	PublicKey  string // may be empty for a seed generated at deploy time
	HomeDomain string // NODE_HOME_DOMAIN
}

// Threshold rules from stellar-core: an org needs a simple majority of its
// validators; a quality level needs 67% of its members (all of them at
// CRITICAL).
func simpleMajority(n int) int { return n/2 + 1 }

func byzantine(n int) int {
	if n == 0 {
		return 0
	}
	return 1 + (n*67-1)/100
}

var levels = []stellarcfg.Quality{stellarcfg.Critical, stellarcfg.High, stellarcfg.Medium, stellarcfg.Low}

// Resolve gives every validator, and the node itself, its effective
// quality: its own QUALITY, else its home domain's. Nodes with no quality
// are returned separately.
func Resolve(cfg *stellarcfg.Config, self Self) (nodes []Node, unrated []Node) {
	domainQ := map[string]stellarcfg.Quality{}
	for _, h := range cfg.HomeDomains {
		domainQ[h.HomeDomain] = h.Quality
	}
	add := func(n Node, q stellarcfg.Quality) {
		if q == "" {
			q = domainQ[n.HomeDomain]
		}
		n.Quality = q
		if !q.Valid() {
			unrated = append(unrated, n)
			return
		}
		nodes = append(nodes, n)
	}
	if self.Name != "" || self.PublicKey != "" {
		add(Node{Name: self.Name, PublicKey: self.PublicKey, HomeDomain: self.HomeDomain, Self: true}, "")
	}
	for _, v := range cfg.Validators {
		add(Node{Name: v.Name, PublicKey: v.PublicKey, HomeDomain: v.HomeDomain, History: v.History}, v.Quality)
	}
	return nodes, unrated
}

// Generate builds the quorum set stellar-core would build for cfg on the
// node self. It returns nil when there is nothing to build.
func Generate(cfg *stellarcfg.Config, self Self) *Set {
	nodes, _ := Resolve(cfg, self)
	byLevel := map[stellarcfg.Quality]map[string][]Node{}
	for _, n := range nodes {
		if byLevel[n.Quality] == nil {
			byLevel[n.Quality] = map[string][]Node{}
		}
		byLevel[n.Quality][n.HomeDomain] = append(byLevel[n.Quality][n.HomeDomain], n)
	}
	// build returns the set for quality level i and below. A level with a
	// single member is that member: an inner set, or a lone node (returned
	// as node instead of set).
	var build func(i int) (set *Set, node *Node)
	build = func(i int) (*Set, *Node) {
		if i >= len(levels) {
			return nil, nil
		}
		q := levels[i]
		set := &Set{Label: string(q)}
		domains := make([]string, 0, len(byLevel[q]))
		for d := range byLevel[q] {
			domains = append(domains, d)
		}
		sort.Strings(domains)
		for _, d := range domains {
			org := byLevel[q][d]
			sort.Slice(org, func(a, b int) bool { return org[a].Name < org[b].Name })
			if len(org) == 1 {
				set.Nodes = append(set.Nodes, org[0])
				continue
			}
			set.Inner = append(set.Inner, &Set{Threshold: simpleMajority(len(org)), Nodes: org, Label: d})
		}
		switch lower, lone := build(i + 1); {
		case lower != nil:
			set.Inner = append(set.Inner, lower)
		case lone != nil:
			set.Nodes = append(set.Nodes, *lone)
		}
		switch {
		case set.Size() == 0:
			return nil, nil
		case set.Size() == 1 && len(set.Inner) == 1:
			return set.Inner[0], nil
		case set.Size() == 1:
			return nil, &set.Nodes[0]
		}
		if q == stellarcfg.Critical {
			set.Threshold = set.Size()
		} else {
			set.Threshold = byzantine(set.Size())
		}
		return set, nil
	}
	set, lone := build(0)
	if lone != nil {
		return &Set{Threshold: 1, Nodes: []Node{*lone}, Label: lone.HomeDomain}
	}
	return set
}

// Lines renders the set as an indented tree for the preview.
func (s *Set) Lines() []string {
	var out []string
	var walk func(s *Set, indent string)
	walk = func(s *Set, indent string) {
		head := fmt.Sprintf("%s%d of %d", indent, s.Threshold, s.Size())
		if s.Label != "" {
			head += "  " + s.Label
		}
		if t := s.Tolerance(); t == 1 {
			head += "  (tolerates 1 failure)"
		} else {
			head += fmt.Sprintf("  (tolerates %d failures)", t)
		}
		out = append(out, head)
		for _, n := range s.Nodes {
			name := n.Name
			if name == "" {
				name = n.PublicKey
			}
			if n.Self {
				name += " (this node)"
			}
			out = append(out, fmt.Sprintf("%s  %s  %s %s", indent, name, n.HomeDomain, n.Quality))
		}
		for _, in := range s.Inner {
			walk(in, indent+"  ")
		}
	}
	walk(s, "")
	return out
}

// Shape is a canonical, order-independent description of a set, for
// comparing with stellar-core's output.
func (s *Set) Shape() string {
	var parts []string
	for _, n := range s.Nodes {
		parts = append(parts, n.Name)
	}
	for _, in := range s.Inner {
		parts = append(parts, in.Shape())
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d:[%s]", s.Threshold, strings.Join(parts, ","))
}
