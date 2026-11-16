package app

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/health"
	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
)

type (
	settingsLoadedMsg struct {
		s   inventory.Settings
		err error
	}
	// pollTickMsg is a host's poll timer firing. gen guards against timers
	// from a previous connection.
	pollTickMsg struct {
		alias string
		gen   int
	}
	infoResultMsg struct {
		alias     string
		gen       int
		info      *corehttp.Info
		raw       []byte
		err       error
		transport string
		at        time.Time
	}
)

func (m Model) loadSettings() tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		s, err := b.LoadSettings()
		return settingsLoadedMsg{s, err}
	}
}

func (m Model) interval() time.Duration {
	if m.fleet.PollInterval > 0 {
		return m.fleet.PollInterval
	}
	return inventory.DefaultSettings().PollInterval()
}

// startPolling begins a fresh poll cycle for alias: any earlier timer is
// orphaned by bumping the generation.
func (m *Model) startPolling(alias string) tea.Cmd {
	m.pollGen[alias]++
	return m.fetchInfo(alias, m.pollGen[alias])
}

func (m *Model) fetchInfo(alias string, gen int) tea.Cmd {
	ih := m.inv.Host(alias)
	if ih == nil || m.backend == nil {
		return nil
	}
	client := m.backend.AdminClient(alias, ih.HTTPPort, ih.ConfigPath, ih.Service)
	now := m.opts.Now
	return func() tea.Msg {
		info, raw, err := client.Info(context.Background()) // the client applies the 5 s timeout
		r := infoResultMsg{alias: alias, gen: gen, raw: raw, err: err, transport: client.Transport().Name(), at: now()}
		if info != nil {
			r.info = &info.Info
		}
		return r
	}
}

// schedule arms alias's next poll: the interval, or 30 s after three
// consecutive failures (SPEC.md "Polling").
func (m Model) schedule(alias string, gen int, failures int) tea.Cmd {
	return tea.Tick(m.pollDelay(failures), func(time.Time) tea.Msg { return pollTickMsg{alias, gen} })
}

func (m Model) pollDelay(failures int) time.Duration {
	if failures >= health.FailuresBeforeSlow {
		return health.SlowRetry
	}
	return m.interval()
}

func (m Model) handlePoll(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case settingsLoadedMsg:
		m.fleet.PollInterval = msg.s.PollInterval()
		if msg.err != nil {
			m.fleet.Flash = "Settings ignored: " + msg.err.Error()
		}
		switch msg.s.Theme {
		case "dark", "light":
			m.themeFixed = true
			m.dark = msg.s.Theme == "dark"
			m.theme = newTheme(m.dark, m.opts.NoColor)
		}
		return m, nil, true

	case ui.TogglePauseMsg:
		m.fleet.Paused = !m.fleet.Paused
		return m, nil, true

	case pollTickMsg:
		if m.pollGen[msg.alias] != msg.gen {
			return m, nil, true // stale timer
		}
		hv := m.fleet.Host(msg.alias)
		if hv == nil {
			return m, nil, true
		}
		if m.fleet.Paused || hv.Conn.State != sshx.StateConnected {
			return m, m.schedule(msg.alias, msg.gen, 0), true
		}
		return m, m.fetchInfo(msg.alias, msg.gen), true

	case infoResultMsg:
		if m.pollGen[msg.alias] != msg.gen {
			return m, nil, true
		}
		hv := m.fleet.Host(msg.alias)
		if hv == nil {
			return m, nil, true
		}
		hv.Transport = msg.transport
		if msg.err != nil {
			hv.PollFailures++
			hv.InfoErr = msg.err.Error()
			return m, m.schedule(msg.alias, msg.gen, hv.PollFailures), true
		}
		prevState := ""
		if hv.Info != nil {
			prevState = hv.Info.State
		}
		hv.Info, hv.InfoRaw, hv.InfoAt, hv.InfoErr, hv.PollFailures = msg.info, msg.raw, msg.at, "", 0
		switch {
		case msg.info.State == health.SyncedState:
			hv.NotSyncedSince = time.Time{}
		case prevState == health.SyncedState || hv.NotSyncedSince.IsZero():
			hv.NotSyncedSince = msg.at
		}
		return m, m.schedule(msg.alias, msg.gen, 0), true

	case ui.FetchMsg:
		return m, m.fetchView(msg), true
	}
	return m, nil, false
}

// fetchView runs an on-demand admin request for a host detail tab.
func (m Model) fetchView(f ui.FetchMsg) tea.Cmd {
	ih := m.inv.Host(f.Alias)
	if ih == nil || m.backend == nil {
		return nil
	}
	client := m.backend.AdminClient(f.Alias, ih.HTTPPort, ih.ConfigPath, ih.Service)
	now := m.opts.Now
	return func() tea.Msg {
		ctx := context.Background()
		r := ui.FetchedMsg{Alias: f.Alias, View: f.View}
		switch f.View {
		case ui.ViewQuorum, ui.ViewQuorumTransitive:
			r.Quorum, r.Raw, r.Err = client.Quorum(ctx, f.View == ui.ViewQuorumTransitive)
		case ui.ViewPeers:
			r.Peers, r.Raw, r.Err = client.Peers(ctx)
		case ui.ViewUpgrades:
			var u *corehttp.UpgradesResponse
			u, r.Raw, r.Err = client.Upgrades(ctx)
			if u != nil {
				r.Upgrades = *u
			}
		}
		r.Transport, r.At = client.Transport().Name(), now()
		return r
	}
}
