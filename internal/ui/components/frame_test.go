package components

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

func TestFrameExactSize(t *testing.T) {
	th := theme.New(true, true)
	for _, size := range [][2]int{{80, 24}, {100, 30}, {120, 40}} {
		out := Frame(th, FrameSpec{
			Width: size[0], Height: size[1],
			Title: "auriga · org acme · pubnet", Right: "14:02:11 UTC",
			Body:   []string{"short", strings.Repeat("wide ", 40)},
			Status: "status", Footer: "q quit",
		})
		lines := strings.Split(out, "\n")
		if len(lines) != size[1] {
			t.Fatalf("%v: %d lines, want %d", size, len(lines), size[1])
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != size[0] {
				t.Errorf("%v: line %d is %d wide, want %d: %q", size, i, w, size[0], l)
			}
		}
	}
}

func TestFrameLongTitleAndFooter(t *testing.T) {
	th := theme.New(false, true)
	out := Frame(th, FrameSpec{
		Width: 80, Height: 24,
		Title: strings.Repeat("t", 200), Right: "R",
		Footer: strings.Repeat("f", 200),
	})
	for i, l := range strings.Split(out, "\n") {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
}

func TestHintsSkipDisabled(t *testing.T) {
	th := theme.New(true, true)
	on := key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit"))
	off := key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add"), key.WithDisabled())
	got := lipgloss.NewStyle().Render(Hints(th, off, on))
	if strings.Contains(got, "add") || !strings.Contains(got, "quit") {
		t.Errorf("Hints = %q", got)
	}
}
