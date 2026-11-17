package upgradesview

import "github.com/owenjacob-dev/auriga/internal/upgrades"

// Requests to the root model, which does the I/O, and its replies.
type (
	// FetchMsg asks for upgrades?mode=get on each host.
	FetchMsg struct{ Aliases []string }
	// FetchedMsg is one host's scheduled values.
	FetchedMsg struct {
		Alias     string
		Scheduled map[string]string
		Err       error
	}
	// ArmMsg arms hosts, in parallel; each answers with a ResultMsg.
	ArmMsg struct {
		Network string
		Params  upgrades.Params
		Aliases []string
	}
	// ClearMsg clears hosts' upgrade votes; each answers with a ResultMsg.
	ClearMsg struct {
		Aliases []string
		Was     map[string]string // what each host was armed with, for the audit log
	}
	// ResultMsg is one host's outcome, confirmed with upgrades?mode=get and
	// the info status line.
	ResultMsg struct {
		Alias     string
		Err       error
		Confirmed bool
		Scheduled map[string]string
		Armed     string
	}
	// ResolveKeyMsg asks a host to resolve a configupgradesetkey with
	// dumpproposedsettings before arming.
	ResolveKeyMsg struct{ Alias, Key string }
	// KeyResolvedMsg answers it.
	KeyResolvedMsg struct {
		Key string
		Err error
	}
	// InspectMsg asks for the settings a proposal would change on a host.
	InspectMsg struct{ Alias, Key string }
	// InspectedMsg answers it.
	InspectedMsg struct {
		Alias, Key string
		Changes    []upgrades.Change
		Err        error
	}
)
