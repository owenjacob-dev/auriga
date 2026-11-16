package app

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/pelletier/go-toml/v2"

	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/ui"
)

// probeTimeout bounds one probe; it is long because a sudo password prompt
// may be waiting for the operator.
const probeTimeout = 5 * time.Minute

type (
	poolEventMsg       struct{ event sshx.Event }
	inventoryLoadedMsg struct {
		inv *inventory.Inventory
		err error
	}
	inventorySavedMsg struct{ err error }
	probeResultMsg    struct {
		alias  string
		seq    int
		facts  *remote.ProbeFacts
		config *remote.RedactedConfig
		err    error
	}
	backendDoneMsg struct{}
)

// listen waits for the next pool event.
func (m Model) listen() tea.Cmd {
	if m.backend == nil {
		return nil
	}
	ch := m.backend.Events()
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return poolEventMsg{e}
	}
}

func (m Model) loadInventory() tea.Cmd {
	b := m.backend
	return func() tea.Msg {
		inv, err := b.LoadInventory()
		return inventoryLoadedMsg{inv, err}
	}
}

func (m Model) saveInventory() tea.Cmd {
	if m.backend == nil || m.inv == nil {
		return nil
	}
	b := m.backend
	snapshot := *m.inv
	snapshot.Orgs = append([]inventory.Org(nil), m.inv.Orgs...)
	snapshot.Hosts = append([]inventory.Host(nil), m.inv.Hosts...)
	return func() tea.Msg { return inventorySavedMsg{b.SaveInventory(&snapshot)} }
}

func (m Model) backendCall(f func(Backend)) tea.Cmd {
	b := m.backend
	return func() tea.Msg { f(b); return backendDoneMsg{} }
}

// syncFleet rebuilds the fleet's host order from the inventory, keeping
// existing host views.
func (m *Model) syncFleet() {
	old := map[string]*ui.HostView{}
	for _, h := range m.fleet.Hosts {
		old[h.Alias] = h
	}
	m.fleet.Hosts = m.fleet.Hosts[:0]
	for _, ih := range m.inv.Hosts {
		hv := old[ih.Alias]
		if hv == nil {
			hv = &ui.HostView{Alias: ih.Alias, Conn: sshx.HostState{Alias: ih.Alias, State: sshx.StateConnecting}}
		}
		hv.Org = ih.Org
		m.fleet.Hosts = append(m.fleet.Hosts, hv)
	}
}

func (m Model) handleFleet(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case inventoryLoadedMsg:
		if msg.err != nil {
			m.fleet.Flash = "Inventory not loaded: " + msg.err.Error()
			m.inv = inventory.New()
			m.invBroken = true // never overwrite a file we could not read
			return m, nil, true
		}
		m.inv = msg.inv
		m.syncFleet()
		var cmds []tea.Cmd
		for _, h := range m.inv.Hosts {
			alias := h.Alias
			cmds = append(cmds, m.backendCall(func(b Backend) { b.Connect(alias) }))
		}
		return m, tea.Batch(cmds...), true

	case inventorySavedMsg:
		if msg.err != nil {
			m.fleet.Flash = "Inventory not saved: " + msg.err.Error()
		} else if m.fleet.Flash != "" && !m.invBroken {
			m.fleet.Flash = ""
		}
		return m, nil, true

	case poolEventMsg:
		cmd := m.handlePoolEvent(msg.event)
		return m, tea.Batch(cmd, m.listen()), true

	case probeResultMsg:
		return m, m.handleProbeResult(msg), true

	case ui.AddHostsMsg:
		if m.inv == nil || m.invBroken {
			return m, nil, true
		}
		var cmds []tea.Cmd
		for _, a := range msg.Aliases {
			added, err := m.inv.AddHost(a)
			if err != nil {
				m.fleet.Flash = err.Error()
				continue
			}
			if added {
				alias := a
				cmds = append(cmds, m.backendCall(func(b Backend) { b.Connect(alias) }))
			}
		}
		m.syncFleet()
		return m, tea.Batch(append(cmds, m.saveInventory())...), true

	case ui.RemoveHostMsg:
		if m.inv == nil || m.invBroken {
			return m, nil, true
		}
		m.inv.RemoveHost(msg.Alias)
		delete(m.pollGen, msg.Alias)
		m.syncFleet()
		m.dropPrompts(msg.Alias)
		alias := msg.Alias
		return m, tea.Batch(m.backendCall(func(b Backend) { b.Disconnect(alias) }), m.saveInventory()), true

	case ui.RefreshHostMsg:
		hv := m.fleet.Host(msg.Alias)
		if hv == nil {
			return m, nil, true
		}
		if hv.Conn.State == sshx.StateConnected {
			alias := msg.Alias
			return m, tea.Batch(
				m.backendCall(func(b Backend) { b.ResetAdmin(alias) }),
				m.startProbe(hv), m.startPolling(alias)), true
		}
		alias := msg.Alias
		return m, m.backendCall(func(b Backend) { b.Reconnect(alias) }), true

	case ui.LoadAliasesMsg:
		if m.backend == nil {
			return m, ui.Emit(ui.AliasesMsg{}), true
		}
		b := m.backend
		return m, func() tea.Msg {
			a, w, err := b.ListAliases()
			return ui.AliasesMsg{Aliases: a, Warning: w, Err: err}
		}, true
	}
	return m, nil, false
}

func (m *Model) handlePoolEvent(e sshx.Event) tea.Cmd {
	switch e := e.(type) {
	case sshx.StateEvent:
		hv := m.fleet.Host(e.Alias)
		if hv == nil {
			return nil
		}
		prev := hv.Conn.State
		hv.Conn = e.HostState
		if e.State == sshx.StateConnected && prev != sshx.StateConnected {
			return tea.Batch(m.startProbe(hv), m.startPolling(hv.Alias))
		}
	case sshx.PromptEvent:
		if m.fleet.Host(e.Alias) == nil {
			e.Cancel()
			return nil
		}
		m.prompts = append(m.prompts, e)
	}
	return nil
}

func (m *Model) startProbe(hv *ui.HostView) tea.Cmd {
	ih := m.inv.Host(hv.Alias)
	if ih == nil || m.backend == nil {
		return nil
	}
	m.probeSeq++
	seq := m.probeSeq
	m.probing[hv.Alias] = seq
	hv.Probing = true
	b, alias, path, svc := m.backend, ih.Alias, ih.ConfigPath, ih.Service
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		res := probeResultMsg{alias: alias, seq: seq}
		facts, err := b.Probe(ctx, alias, path, svc)
		if err != nil {
			res.err = err
			return res
		}
		res.facts = &facts
		if facts.ConfigPresent {
			rc, err := b.ReadConfig(ctx, alias, path)
			if err != nil && !errors.Is(err, remote.ErrNoConfig) {
				res.err = err
				return res
			}
			if err == nil {
				res.config = &rc
			}
		}
		return res
	}
}

func (m *Model) handleProbeResult(r probeResultMsg) tea.Cmd {
	hv := m.fleet.Host(r.alias)
	if hv == nil || m.probing[r.alias] != r.seq {
		return nil // removed, or a newer probe is running
	}
	hv.Probing = false
	hv.ProbedAt = m.opts.Now()
	if r.err != nil {
		hv.ProbeErr = r.err.Error()
		return nil
	}
	hv.ProbeErr = ""
	hv.Probe, hv.Config = r.facts, r.config
	ih := m.inv.Host(r.alias)
	if ih == nil {
		return nil
	}
	changed := false
	if r.facts.HTTPPort != 0 && ih.HTTPPort != r.facts.HTTPPort {
		ih.HTTPPort, changed = r.facts.HTTPPort, true
	}
	if c := r.config; c != nil {
		if c.PublicKey != "" && ih.PublicKey != c.PublicKey {
			ih.PublicKey, changed = c.PublicKey, true
		}
		if c.NodeAlias != "" && ih.NodeName == "" {
			ih.NodeName, changed = c.NodeAlias, true
		}
		hv.HomeDomain, hv.Network = peekConfig(c.Text)
		before := ih.Org
		m.inv.GroupByHomeDomain(r.alias, hv.HomeDomain, hv.Network)
		if m.inv.Host(r.alias).Org != before {
			changed = true
		}
		m.syncFleet()
	}
	if changed && !m.invBroken {
		return m.saveInventory()
	}
	return nil
}

// peekConfig reads NODE_HOME_DOMAIN and the network from a redacted config.
// Full parsing arrives with the config model (M3); a config that does not
// parse simply yields nothing here.
func peekConfig(text []byte) (homeDomain, network string) {
	var c struct {
		HomeDomain string `toml:"NODE_HOME_DOMAIN"`
		Passphrase string `toml:"NETWORK_PASSPHRASE"`
	}
	if err := toml.Unmarshal(text, &c); err != nil {
		return "", ""
	}
	return c.HomeDomain, inventory.NetworkFromPassphrase(c.Passphrase)
}

func (m *Model) dropPrompts(alias string) {
	kept := m.prompts[:0]
	for _, p := range m.prompts {
		if p.Alias == alias {
			p.Cancel()
			continue
		}
		kept = append(kept, p)
	}
	m.prompts = kept
}
