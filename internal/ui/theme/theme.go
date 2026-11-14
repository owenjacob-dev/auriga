// Package theme holds Auriga's colours, text styles and status symbols.
//
// Status is never shown by colour alone (SPEC.md "Display rules"): every
// status has a symbol and a word, and NO_COLOR removes all colour while
// keeping layout and symbols intact.
package theme

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Theme is the set of styles one frame is drawn with. Build it with New.
type Theme struct {
	Dark    bool
	NoColor bool

	Border lipgloss.Style // frame lines
	Title  lipgloss.Style // app name in the top border
	Text   lipgloss.Style
	Muted  lipgloss.Style // secondary text, empty states
	Header lipgloss.Style // table column headings
	Key    lipgloss.Style // key in a hint ("q")
	Desc   lipgloss.Style // description in a hint ("quit")
	Banner lipgloss.Style // red alert bar

	OK, Warn, Bad, Stale lipgloss.Style
}

// New returns the theme for a dark or light terminal background. With noColor
// set (NO_COLOR present in the environment) no style carries a colour.
func New(dark, noColor bool) Theme {
	ld := lipgloss.LightDark(dark)
	c := func(light, darkHex string) color.Color {
		if noColor {
			return lipgloss.NoColor{}
		}
		return ld(lipgloss.Color(light), lipgloss.Color(darkHex))
	}
	s := lipgloss.NewStyle
	t := Theme{
		Dark:    dark,
		NoColor: noColor,
		Border:  s().Foreground(c("#8a93a6", "#4b5468")),
		Title:   s().Bold(true).Foreground(c("#2b4a8e", "#93acea")),
		Text:    s(),
		Muted:   s().Foreground(c("#5b6477", "#98a3b8")),
		Header:  s().Bold(true),
		Key:     s().Bold(true).Foreground(c("#2b4a8e", "#93acea")),
		Desc:    s().Foreground(c("#5b6477", "#98a3b8")),
		Banner:  s().Bold(true).Foreground(c("#ffffff", "#ffffff")).Background(c("#b3261e", "#9c2a22")),
		OK:      s().Foreground(c("#1f7a4d", "#5cc38f")),
		Warn:    s().Foreground(c("#9a6200", "#e6a94a")),
		Bad:     s().Bold(true).Foreground(c("#b3261e", "#f08a80")),
		Stale:   s().Foreground(c("#9aa1ad", "#5d6678")),
	}
	if noColor {
		// Keep the alert legible without colour: reverse video instead of red.
		t.Banner = s().Bold(true).Reverse(true)
		t.Stale = s().Faint(true)
	}
	return t
}

// Status is a health level shown as symbol + word + colour.
type Status int

const (
	StatusUnknown Status = iota
	StatusOK
	StatusDegraded
	StatusDown
)

// Symbol returns the status glyph: ● ok, ◐ degraded, ○ down, · unknown.
func (s Status) Symbol() string {
	switch s {
	case StatusOK:
		return "●"
	case StatusDegraded:
		return "◐"
	case StatusDown:
		return "○"
	default:
		return "·"
	}
}

// Word returns the status word that accompanies the symbol.
func (s Status) Word() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusDegraded:
		return "degraded"
	case StatusDown:
		return "down"
	default:
		return "unknown"
	}
}

// Style returns the style for a status.
func (t Theme) Style(s Status) lipgloss.Style {
	switch s {
	case StatusOK:
		return t.OK
	case StatusDegraded:
		return t.Warn
	case StatusDown:
		return t.Bad
	default:
		return t.Muted
	}
}
