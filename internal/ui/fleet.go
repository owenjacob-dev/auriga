package ui

import (
	"regexp"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

// HostView is everything the UI knows about one host. The root model owns
// it; screens read it through Context.Fleet.
type HostView struct {
	Alias string
	Org   string
	Conn  sshx.HostState

	Probing  bool
	Probe    *remote.ProbeFacts
	Config   *remote.RedactedConfig // nil until read; Config.Text is redacted
	ProbeErr string
	ProbedAt time.Time

	HomeDomain string // from the config, when it parses
	Network    string // "testnet", "pubnet" or ""

	// Config model (set after each probe).
	Class   *stellarcfg.Classification
	Issues  []stellarcfg.Issue // validation, for adoptable configs
	Backups []remote.Backup

	// Admin endpoint polling.
	Info           *corehttp.Info // last successful info; kept when polls fail
	InfoRaw        []byte         // its JSON, for the Info tab and copy
	InfoAt         time.Time
	InfoErr        string // last poll error, "" after a success
	PollFailures   int    // consecutive failures
	NotSyncedSince time.Time
	Transport      string // "ssh tunnel" or "on host (curl)" …

	// Deploying is set while a deploy runs on the host; dashboard polling
	// pauses (the deploy checks the host itself).
	Deploying bool
}

// Fleet is the ordered set of hosts in the inventory.
type Fleet struct {
	Hosts []*HostView
	// ResolverWarning is set when ~/.ssh/config is read without OpenSSH.
	ResolverWarning string
	// Flash is a one-line message for the status bar (e.g. a save error).
	Flash string
	// Paused is true while polling is paused (p on the dashboard).
	Paused bool
	// PollInterval is the configured poll interval.
	PollInterval time.Duration
}

// Host returns the host with alias, or nil.
func (f *Fleet) Host(alias string) *HostView {
	if f == nil {
		return nil
	}
	for _, h := range f.Hosts {
		if h.Alias == alias {
			return h
		}
	}
	return nil
}

// Intents: messages screens return (as commands) to ask the root model to
// act. Screens never do I/O themselves.
type (
	// OpenHostPickerMsg opens the host picker.
	OpenHostPickerMsg struct{}
	// AddHostsMsg adds aliases to the inventory and connects to them.
	AddHostsMsg struct{ Aliases []string }
	// RemoveHostMsg removes a host from the local inventory only.
	RemoveHostMsg struct{ Alias string }
	// RefreshHostMsg probes a connected host again, or reconnects a
	// disconnected one.
	RefreshHostMsg struct{ Alias string }
	// OpenHostMsg opens the detail view for a host.
	OpenHostMsg struct{ Alias string }
	// LoadAliasesMsg asks for the alias list from ~/.ssh/config.
	LoadAliasesMsg struct{}
	// AliasesMsg carries the alias list back to the host picker.
	AliasesMsg struct {
		Aliases []string
		Warning string
		Err     error
	}
	// CloseMsg closes the screen that returned it.
	CloseMsg struct{}
	// TogglePauseMsg pauses or resumes polling.
	TogglePauseMsg struct{}
	// OpenRestoreMsg opens the manual rollback screen for a host.
	OpenRestoreMsg struct{ Alias string }
	// OpenAuditMsg opens the audit log.
	OpenAuditMsg struct{}
	// OpenWizardMsg opens the setup wizard for an org (Org "" for a new
	// one), preselecting Hosts.
	OpenWizardMsg struct {
		Org   string
		Hosts []string
	}
	// FetchMsg asks for one admin view of a host (host detail tabs).
	FetchMsg struct {
		Alias string
		View  AdminView
	}
	// FetchedMsg carries a FetchMsg's result back to the screen.
	FetchedMsg struct {
		Alias     string
		View      AdminView
		Raw       []byte
		Quorum    *corehttp.QuorumResponse
		Peers     *corehttp.PeersResponse
		Upgrades  corehttp.UpgradesResponse
		Err       error
		Transport string
		At        time.Time
	}
)

// AdminView names an on-demand admin request.
type AdminView int

const (
	ViewQuorum AdminView = iota
	ViewQuorumTransitive
	ViewPeers
	ViewUpgrades
)

// Emit wraps a message as a command.
func Emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// TextCapture is implemented by screens that are taking typed text (a filter
// box, a form field). While it returns true, the root model passes every key
// except ctrl+c to the screen instead of treating q and ? as global keys.
type TextCapture interface {
	CapturingText() bool
}

var versionRE = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

// CoreVersion extracts "30.0.0" from `stellar-core version` output.
func CoreVersion(raw string) string {
	if m := versionRE.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

// FullHelp is implemented by screens whose help overlay lists more keys than
// fit in the footer hints.
type FullHelp interface {
	FullHelp() []key.Binding
}

// FleetAware is implemented by screens that read the fleet outside View (for
// example to move a selection). The root model calls SetFleet when it opens
// the screen; the pointer stays valid for the program's life.
type FleetAware interface {
	SetFleet(*Fleet)
}
