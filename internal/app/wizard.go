package app

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

// handleWizard performs the wizard's I/O requests.
func (m Model) handleWizard(msg tea.Msg) (Model, tea.Cmd, bool) {
	b := m.backend
	switch msg := msg.(type) {
	case ui.OpenWizardMsg:
		org := msg.Org
		if org == "" && len(msg.Hosts) > 0 {
			org = strings.SplitN(msg.Hosts[0], "-", 2)[0]
		}
		return m, m.Push(wizard.New(org, msg.Hosts, m.opts.Version, m.opts.Now)), true
	case wizard.LoadMsg:
		if b == nil {
			return m, ui.Emit(wizard.LoadedMsg{}), true
		}
		return m, func() tea.Msg {
			d, ok, err := b.LoadDraft(msg.Org)
			if !ok {
				d = nil
			}
			return wizard.LoadedMsg{Draft: d, Err: err}
		}, true
	case wizard.LoadPresetMsg:
		if b == nil {
			return m, nil, true
		}
		return m, func() tea.Msg {
			p, err := b.LoadPreset(msg.Network)
			return wizard.PresetMsg{Preset: p, Err: err}
		}, true
	case wizard.SaveDraftMsg:
		if b == nil {
			return m, nil, true
		}
		return m, func() tea.Msg {
			if err := b.SaveDraft(msg.Draft); err != nil {
				return flashMsg{"Draft not saved: " + err.Error()}
			}
			return nil
		}, true
	case wizard.FetchPresetMsg:
		return m, func() tea.Msg {
			body, err := b.FetchPreset(context.Background(), msg.Network)
			return wizard.FetchedPresetMsg{Network: msg.Network, Body: body, Err: err}
		}, true
	case wizard.SavePresetMsg:
		return m, func() tea.Msg {
			if err := b.SavePreset(msg.Preset); err != nil {
				return flashMsg{"Preset not saved: " + err.Error()}
			}
			return nil
		}, true
	case wizard.CheckArchiveMsg:
		if b == nil {
			return m, nil, true
		}
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			c, err := b.CheckArchive(ctx, msg.Alias, msg.Spec, msg.Publish)
			return wizard.ArchiveCheckedMsg{Alias: msg.Alias, Check: c, Err: err}
		}, true
	case flashMsg:
		m.fleet.Flash = msg.text
		return m, nil, true
	}
	return m, nil, false
}

type flashMsg struct{ text string }
