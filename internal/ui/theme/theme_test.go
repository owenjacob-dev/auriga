package theme

import (
	"regexp"
	"testing"
)

// colourSGR matches SGR parameters that set a foreground or background colour.
var colourSGR = regexp.MustCompile(`\x1b\[[0-9;]*(3[0-9]|4[0-9]|9[0-7]|10[0-7])(;[0-9;]*)?m`)

func TestNoColorHasNoColour(t *testing.T) {
	th := New(true, true)
	for name, out := range map[string]string{
		"border": th.Border.Render("x"), "title": th.Title.Render("x"), "ok": th.OK.Render("x"),
		"warn": th.Warn.Render("x"), "bad": th.Bad.Render("x"), "banner": th.Banner.Render("x"),
		"muted": th.Muted.Render("x"), "key": th.Key.Render("x"), "stale": th.Stale.Render("x"),
	} {
		if colourSGR.MatchString(out) {
			t.Errorf("%s: NO_COLOR output contains a colour sequence: %q", name, out)
		}
	}
}

func TestStatusesAreDistinctWithoutColour(t *testing.T) {
	seenSym, seenWord := map[string]bool{}, map[string]bool{}
	for _, s := range []Status{StatusUnknown, StatusOK, StatusDegraded, StatusDown} {
		if seenSym[s.Symbol()] || seenWord[s.Word()] {
			t.Errorf("status %d reuses a symbol or word", s)
		}
		seenSym[s.Symbol()], seenWord[s.Word()] = true, true
	}
}
