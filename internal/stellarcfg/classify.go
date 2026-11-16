package stellarcfg

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// Class is how Auriga may treat a host's config (SPEC.md "Classification").
type Class int

const (
	// Unconfigured: no config yet; run the setup wizard.
	Unconfigured Class = iota
	// Adoptable: the config parses into the model; view, edit, deploy.
	Adoptable
	// ReadOnly: the config does not parse, or uses features the model does
	// not cover; monitor and arm upgrades only.
	ReadOnly
)

func (c Class) String() string {
	switch c {
	case Unconfigured:
		return "unconfigured"
	case Adoptable:
		return "adoptable"
	}
	return "read-only"
}

// Classification is the verdict on one host's config.
type Classification struct {
	Class   Class
	Reasons []string // why it is read-only
	Result  *Result  // nil when unconfigured or unparseable
}

// Classify decides from the redacted config text. present is false when
// the file does not exist. The stellar-core package ships no
// stellar-core.cfg (only examples), so "the package default" is an absent
// or empty file.
func Classify(present bool, text []byte) Classification {
	if !present || onlyComments(text) {
		return Classification{Class: Unconfigured}
	}
	res, err := Parse(text)
	if err != nil {
		return Classification{Class: ReadOnly, Reasons: []string{"does not parse: " + err.Error()}}
	}
	if len(res.Unsupported) > 0 {
		return Classification{Class: ReadOnly, Reasons: res.Unsupported, Result: res}
	}
	return Classification{Class: Adoptable, Result: res}
}

func onlyComments(text []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(text))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			return false
		}
	}
	return true
}

// Diff is a unified diff from old to new; "" when they are equal.
func Diff(oldName, newName string, old, new []byte) string {
	return udiff.Unified(oldName, newName, string(old), string(new))
}

// AdoptionNotes explains what a first deploy to an adopted host changes
// besides the settings themselves (SPEC.md "Classification": keys the tool
// will reorder and comments that will be lost).
func AdoptionNotes(res *Result) []string {
	var notes []string
	if res.Comments > 0 {
		notes = append(notes, fmt.Sprintf("%d comment lines will be dropped; the backup taken before the deploy keeps them", res.Comments))
	}
	notes = append(notes, "settings are rewritten in Auriga's fixed order")
	var kept []string
	for _, e := range res.Config.Unknown {
		kept = append(kept, e.Key)
	}
	if len(kept) > 0 {
		notes = append(notes, fmt.Sprintf("%d other settings are kept unchanged: %s", len(kept), strings.Join(kept, ", ")))
	}
	return notes
}
