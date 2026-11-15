package app

import "charm.land/bubbles/v2/key"

// globalKeys are active on every screen (SPEC.md "Keybindings").
type globalKeys struct {
	Help, Quit, Back key.Binding
}

func newGlobalKeys() globalKeys {
	return globalKeys{
		Help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}

// confirmKeys answer the quit confirmation.
var (
	keyYes = key.NewBinding(key.WithKeys("y", "Y"), key.WithHelp("y", "quit anyway"))
	keyNo  = key.NewBinding(key.WithKeys("n", "N", "esc"), key.WithHelp("n", "keep running"))
)
