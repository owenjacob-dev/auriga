// Package dashboard is the home screen: one row per host with live health.
//
// M0 renders the frame, the column headings and the empty state; hosts,
// polling and the column logic arrive with the SSH layer and the admin
// endpoint client.
package dashboard

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/components"
)

// columns are the headings from SPEC.md's dashboard sketch with their widths.
var columns = []struct {
	title string
	width int
}{
	{"HOST", 10}, {"STATE", 14}, {"LEDGER", 10}, {"AGE", 5}, {"PEERS", 7},
	{"QUORUM", 9}, {"INT", 5}, {"PROTO", 7}, {"UPGRADE", 7},
}

// Model is the dashboard screen.
type Model struct{}

// New returns an empty dashboard.
func New() *Model { return &Model{} }

// Init implements ui.Screen.
func (m *Model) Init() tea.Cmd { return nil }

// Update implements ui.Screen.
func (m *Model) Update(tea.Msg) (ui.Screen, tea.Cmd) { return m, nil }

// Keys implements ui.Screen. Bindings appear here as their features land.
func (m *Model) Keys() []key.Binding { return nil }

// Busy implements ui.Screen.
func (m *Model) Busy() string { return "" }

// View implements ui.Screen.
func (m *Model) View(ctx ui.Context) ui.Layout {
	t := ctx.Theme
	var header string
	for _, c := range columns {
		header += components.Fit(c.title, c.width)
	}
	return ui.Layout{
		Body: []string{
			t.Header.Render(header),
			"",
			t.Muted.Render("No hosts yet."),
		},
		Status: t.Muted.Render("No hosts in the inventory"),
	}
}
