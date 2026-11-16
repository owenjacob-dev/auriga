package wizard

import (
	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// Requests the wizard sends to the root model, which does the I/O, and the
// replies it gets back.
type (
	// LoadMsg asks for the org's saved draft and the network's preset.
	LoadMsg struct {
		Org     string
		Network presets.Network
	}
	// LoadedMsg carries a saved draft (Draft nil when there is none).
	LoadedMsg struct {
		Draft *Draft
		Err   error
	}
	// PresetMsg carries a network's preset.
	PresetMsg struct {
		Preset *presets.Preset
		Err    error
	}
	// SaveDraftMsg saves the draft.
	SaveDraftMsg struct{ Draft *Draft }
	// LoadPresetMsg asks for a network's preset.
	LoadPresetMsg struct{ Network presets.Network }
	// FetchPresetMsg downloads a preset's upstream file (operator-triggered).
	FetchPresetMsg struct{ Network presets.Network }
	// FetchedPresetMsg carries the download.
	FetchedPresetMsg struct {
		Network presets.Network
		Body    []byte
		Err     error
	}
	// SavePresetMsg saves a refreshed preset as the override.
	SavePresetMsg struct{ Preset *presets.Preset }
	// CheckArchiveMsg checks an archive's programs and existence on a host.
	CheckArchiveMsg struct {
		Alias   string
		Spec    stellarcfg.ArchiveSpec
		Publish bool
	}
	// ArchiveCheckedMsg carries the result.
	ArchiveCheckedMsg struct {
		Alias string
		Check remote.ArchiveCheck
		Err   error
	}
)
