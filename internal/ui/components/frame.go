// Package components holds drawing helpers shared by screens.
package components

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// FrameSpec describes the app frame:
//
//	┌ title ──────────────── right ┐
//	│ body                         │
//	├──────────────────────────────┤
//	│ status                       │
//	└ footer ──────────────────────┘
type FrameSpec struct {
	Width, Height int
	Title, Right  string // already styled
	Body          []string
	Status        string
	Footer        string // already styled
}

// FrameChrome is the number of rows and columns the frame itself uses.
const (
	FrameChromeRows = 4 // top, separator, status, bottom
	FrameChromeCols = 4 // "│ " and " │"
)

// Frame draws the app frame. Body lines beyond Height-FrameChromeRows are
// dropped; lines wider than the frame are truncated with an ellipsis.
func Frame(t theme.Theme, f FrameSpec) string {
	w := f.Width
	inner := w - FrameChromeCols
	rows := f.Height - FrameChromeRows
	b := t.Border.Render

	var out strings.Builder
	out.Grow(w * f.Height * 2)

	// Top border with title on the left and Right on the right.
	title, right := f.Title, f.Right
	fill := w - 6 - lipgloss.Width(title) - lipgloss.Width(right)
	if fill < 1 {
		title = ansi.Truncate(title, max(0, lipgloss.Width(title)+fill-1), "…")
		fill = w - 6 - lipgloss.Width(title) - lipgloss.Width(right)
	}
	out.WriteString(b("┌ ") + title + b(" "+strings.Repeat("─", max(0, fill))+" ") + right + b(" ┐") + "\n")

	line := func(s string) {
		s = Fit(s, inner)
		out.WriteString(b("│ ") + s + b(" │") + "\n")
	}
	for i := range rows {
		if i < len(f.Body) {
			line(f.Body[i])
		} else {
			line("")
		}
	}
	out.WriteString(b("├"+strings.Repeat("─", w-2)+"┤") + "\n")
	line(f.Status)

	footer := f.Footer
	fill = w - 4 - lipgloss.Width(footer)
	if fill < 1 {
		footer = ansi.Truncate(footer, max(0, w-5), "…")
		fill = w - 4 - lipgloss.Width(footer)
	}
	out.WriteString(b("└ ") + footer + b(" "+strings.Repeat("─", max(0, fill))+"┘"))
	return out.String()
}

// Fit truncates s to width cells (with an ellipsis) or pads it with spaces.
func Fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// Hints renders enabled bindings as "a add  s setup  ? help".
func Hints(t theme.Theme, bindings ...key.Binding) string {
	parts := make([]string, 0, len(bindings))
	for _, kb := range bindings {
		if !kb.Enabled() {
			continue
		}
		h := kb.Help()
		parts = append(parts, t.Key.Render(h.Key)+" "+t.Desc.Render(h.Desc))
	}
	return strings.Join(parts, "  ")
}
