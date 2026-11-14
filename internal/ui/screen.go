// Package ui holds the screens and the contract between them and the root
// model in internal/app.
//
// Screens never do I/O in Update (CLAUDE.md hard rule 5); they return tea.Cmds
// and receive results as messages.
package ui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/ui/theme"
)

// Context is what a screen needs to draw one frame.
type Context struct {
	Theme theme.Theme
	// Cols and Rows are the size of the body area inside the app frame.
	Cols, Rows int
	Now        time.Time
}

// Layout is a screen's contribution to the app frame. The root model draws
// the border, the clock, the key hints and any overlay around it.
type Layout struct {
	// Subtitle follows the app name in the top border, e.g. "org acme · pubnet".
	Subtitle string
	// Body holds at most Context.Rows lines; longer lines are truncated.
	Body []string
	// Status is the one-line status bar under the body.
	Status string
}

// Screen is one full-screen view in the router stack.
type Screen interface {
	// Init returns the screen's first command, if any.
	Init() tea.Cmd
	// Update handles a message the root model did not consume.
	Update(msg tea.Msg) (Screen, tea.Cmd)
	// View renders the screen for one frame.
	View(ctx Context) Layout
	// Keys lists the screen's own bindings, in hint order. Disabled bindings
	// are hidden from hints and help.
	Keys() []key.Binding
	// Busy returns a non-empty reason when quitting now would interrupt work,
	// such as a running deploy; the root model then asks before quitting.
	Busy() string
}
