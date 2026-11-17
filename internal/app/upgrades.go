package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/audit"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/upgradesview"
	"github.com/owenjacob-dev/auriga/internal/upgrades"
)

// upgradeTimeout bounds one host's arm or clear, with its confirmation.
const upgradeTimeout = 45 * time.Second

// armRecord is a vote armed in this session, watched until it applies or
// a restart drops it (SPEC.md "After arming").
type armRecord struct {
	params    upgrades.Params
	network   string
	startedOn string // info.startedOn when last seen armed
}

type (
	// armCheckMsg is upgrades?mode=get after a watched host stopped
	// reporting its vote.
	armCheckMsg struct {
		alias     string
		scheduled map[string]string
		err       error
	}
	auditDoneMsg struct{ err error }
)

func (m Model) adminFor(alias string) *corehttp.Client {
	ih := m.inv.Host(alias)
	if ih == nil || m.backend == nil {
		return nil
	}
	return m.backend.AdminClient(alias, ih.HTTPPort, ih.ConfigPath, ih.Service)
}

func (m Model) auditCmd(e audit.Entry) tea.Cmd {
	if m.backend == nil {
		return nil
	}
	l := m.backend.AuditLog()
	return func() tea.Msg { return auditDoneMsg{l.Append(e)} }
}

// handleUpgrades runs the upgrades screen's requests.
func (m Model) handleUpgrades(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case ui.OpenUpgradesMsg:
		return m, m.Push(upgradesview.New(msg.Org, m.opts.Now, nil).WithNoColor(m.opts.NoColor)), true

	case upgradesview.FetchMsg:
		var cmds []tea.Cmd
		for _, a := range msg.Aliases {
			c, alias := m.adminFor(a), a
			if c == nil {
				continue
			}
			cmds = append(cmds, func() tea.Msg {
				u, _, err := c.Upgrades(context.Background())
				r := upgradesview.FetchedMsg{Alias: alias, Err: err}
				if u != nil {
					r.Scheduled = u.Scheduled()
				}
				return r
			})
		}
		return m, tea.Batch(cmds...), true

	case upgradesview.ArmMsg:
		var cmds []tea.Cmd
		for _, a := range msg.Aliases {
			c, alias, p := m.adminFor(a), a, msg.Params
			if c == nil {
				continue
			}
			l, org := m.backend.AuditLog(), m.inv.Host(a).Org
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), upgradeTimeout)
				defer cancel()
				r := upgradesview.ResultMsg{Alias: alias}
				r.Err = c.ArmUpgrades(ctx, p.Query())
				e := audit.Entry{Host: alias, Org: org, Network: msg.Network, Action: "upgrade.arm", Detail: p.String(), Result: audit.OK}
				if r.Err != nil {
					e.Result, e.Error = audit.Failed, r.Err.Error()
				}
				if err := l.Append(e); err != nil && r.Err == nil {
					r.Err = fmt.Errorf("armed, but the audit log failed: %w", err)
				}
				if r.Err != nil {
					return r
				}
				confirm(ctx, c, &r)
				r.Confirmed = p.Matches(upgrades.FromScheduled(r.Scheduled)) && r.Armed != ""
				return r
			})
		}
		return m, tea.Batch(cmds...), true

	case upgradesview.ClearMsg:
		var cmds []tea.Cmd
		for _, a := range msg.Aliases {
			c, alias, was := m.adminFor(a), a, msg.Was[a]
			if c == nil {
				continue
			}
			l, org, network := m.backend.AuditLog(), m.inv.Host(a).Org, ""
			if hv := m.fleet.Host(a); hv != nil {
				network = hv.Network
			}
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), upgradeTimeout)
				defer cancel()
				r := upgradesview.ResultMsg{Alias: alias}
				r.Err = c.ClearUpgrades(ctx)
				e := audit.Entry{Host: alias, Org: org, Network: network, Action: "upgrade.clear", Detail: "was: " + was, Result: audit.OK}
				if r.Err != nil {
					e.Result, e.Error = audit.Failed, r.Err.Error()
				}
				if err := l.Append(e); err != nil && r.Err == nil {
					r.Err = fmt.Errorf("cleared, but the audit log failed: %w", err)
				}
				if r.Err != nil {
					return r
				}
				confirm(ctx, c, &r)
				r.Confirmed = len(r.Scheduled) == 0 && r.Armed == ""
				return r
			})
		}
		return m, tea.Batch(cmds...), true

	case upgradesview.ResultMsg:
		if hv := m.fleet.Host(msg.Alias); hv != nil && msg.Err == nil {
			if v, ok := m.topUpgrades(); ok && v.Action() == "arm" {
				startedOn := ""
				if hv.Info != nil {
					startedOn = hv.Info.StartedOn
				}
				m.arms[msg.Alias] = &armRecord{params: v.Params(), network: v.Network(), startedOn: startedOn}
				hv.ArmLost = nil
			} else {
				delete(m.arms, msg.Alias)
				hv.ArmLost = nil
			}
		}
		return m, m.toTop(msg), true

	case upgradesview.ResolveKeyMsg:
		c := m.adminFor(msg.Alias)
		if c == nil {
			return m, nil, true
		}
		return m, func() tea.Msg {
			_, err := c.ProposedSettings(context.Background(), msg.Key)
			var ae *corehttp.Error
			if errors.As(err, &ae) && ae.Body != "" {
				err = errors.New(msg.Alias + " says: " + ae.Body) // the key is already on screen
			}
			return upgradesview.KeyResolvedMsg{Key: msg.Key, Err: err}
		}, true

	case upgradesview.InspectMsg:
		c := m.adminFor(msg.Alias)
		if c == nil {
			return m, nil, true
		}
		return m, func() tea.Msg {
			r := upgradesview.InspectedMsg{Alias: msg.Alias, Key: msg.Key}
			prop, err := c.ProposedSettings(context.Background(), msg.Key)
			if err != nil {
				r.Err = fmt.Errorf("dumpproposedsettings: %w", err)
				return r
			}
			cur, err := c.SorobanInfo(context.Background())
			if err != nil {
				r.Err = fmt.Errorf("sorobaninfo: %w", err)
				return r
			}
			r.Changes, r.Err = upgrades.DiffSettings(cur, prop)
			return r
		}, true

	case upgradesview.FetchedMsg:
		return m, m.toTop(msg), true

	case armCheckMsg:
		hv, rec := m.fleet.Host(msg.alias), m.arms[msg.alias]
		if hv == nil || rec == nil || msg.err != nil || len(msg.scheduled) > 0 {
			return m, nil, true
		}
		p := rec.params
		hv.ArmLost = &p
		delete(m.arms, msg.alias)
		m.fleet.Flash = msg.alias + " no longer has the upgrade vote armed this session; u then R re-arms it."
		return m, nil, true

	case auditDoneMsg:
		if msg.err != nil {
			m.fleet.Flash = "Audit log: " + msg.err.Error()
		}
		return m, nil, true
	}
	return m, nil, false
}

// confirm reads back upgrades?mode=get and the info status line.
func confirm(ctx context.Context, c *corehttp.Client, r *upgradesview.ResultMsg) {
	if u, _, err := c.Upgrades(ctx); err == nil {
		r.Scheduled = u.Scheduled()
	}
	if i, _, err := c.Info(ctx); err == nil {
		r.Armed = i.Info.Armed()
	}
}

func (m Model) topUpgrades() (*upgradesview.Model, bool) {
	for i := len(m.stack) - 1; i >= 0; i-- {
		if v, ok := m.stack[i].(*upgradesview.Model); ok {
			return v, true
		}
	}
	return nil, false
}

// toTop passes msg to the upgrades screen wherever it is in the stack.
func (m *Model) toTop(msg tea.Msg) tea.Cmd {
	for i, s := range m.stack {
		if v, ok := s.(*upgradesview.Model); ok {
			next, cmd := v.Update(msg)
			m.stack[i] = next
			return cmd
		}
	}
	return nil
}

// trackArm follows a vote armed this session on each info poll: it is
// recorded in the audit log when the ledger shows it applied, and checked
// with upgrades?mode=get when the host stops reporting it (after a restart
// or an expiry), to offer re-arming.
func (m *Model) trackArm(alias string, i *corehttp.Info) tea.Cmd {
	rec := m.arms[alias]
	if rec == nil || i == nil {
		return nil
	}
	if i.Armed() != "" {
		rec.startedOn = i.StartedOn
		return nil
	}
	if applied(rec.params, i) {
		delete(m.arms, alias)
		m.fleet.Flash = alias + ": the network upgrade applied (" + strings.TrimPrefix(rec.params.String(), "upgrades?mode=set&") + ")."
		org := ""
		if ih := m.inv.Host(alias); ih != nil {
			org = ih.Org
		}
		return tea.Batch(m.auditCmd(audit.Entry{Host: alias, Org: org, Network: rec.network, Action: "upgrade.applied",
			Detail: fmt.Sprintf("%s; ledger %d at protocol %d", rec.params.String(), i.Ledger.Num, i.Ledger.Version), Result: audit.OK}),
			ui.Emit(upgradesview.FetchMsg{Aliases: []string{alias}})) // the overview shows mode=get again
	}
	c := m.adminFor(alias)
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		u, _, err := c.Upgrades(context.Background())
		r := armCheckMsg{alias: alias, err: err}
		if u != nil {
			r.scheduled = u.Scheduled()
		}
		return r
	}
}

// applied reports whether the ledger in i carries every value of p that
// info shows. A vote of only values info does not show counts as applied
// once its time has passed.
func applied(p upgrades.Params, i *corehttp.Info) bool {
	seen := map[string]int64{
		"protocolversion":     int64(i.Ledger.Version),
		"basefee":             i.Ledger.BaseFee,
		"basereserve":         i.Ledger.BaseReserve,
		"maxtxsetsize":        i.Ledger.MaxTxSetSize,
		"maxsorobantxsetsize": i.Ledger.MaxSorobanTxSetSize,
	}
	checked := 0
	for k, v := range p.Values {
		got, ok := seen[k]
		if !ok {
			continue
		}
		checked++
		if got != int64(v) {
			return false
		}
	}
	if checked == 0 {
		return i.Ledger.CloseTime >= p.Time.Unix()
	}
	return true
}
