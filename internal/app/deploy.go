package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/quorum"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/auditview"
	"github.com/owenjacob-dev/auriga/internal/ui/deployview"
	"github.com/owenjacob-dev/auriga/internal/ui/restoreview"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

// pendingDeploy is a reviewed plan waiting on the deploy screen's
// confirmation. It owns the copies of imported seeds.
type pendingDeploy struct {
	targets []deploy.Target
	opt     deploy.Options
}

func (p *pendingDeploy) zero() {
	for _, t := range p.targets {
		t.Import.Zero()
	}
}

// deployRun is a running deploy.
type deployRun struct {
	c      *deploy.Coordinator
	events chan tea.Msg
	cancel context.CancelFunc
	hosts  []string
}

// hostRef is what a target needs from the inventory, copied so the
// coordinator's re-plan never reads the model.
type hostRef struct {
	configPath, service string
	running             bool
}

// targetsFromPlan turns a plan into deploy targets. seeds holds the
// imported seeds; nil when re-planning (the coordinator keeps them).
func targetsFromPlan(p *wizard.Plan, hosts map[string]hostRef, seeds map[string]*secret.Buffer) ([]deploy.Target, error) {
	var out []deploy.Target
	for _, hp := range p.Hosts {
		ref, ok := hosts[hp.Alias]
		if !ok {
			return nil, fmt.Errorf("%s is not in the inventory", hp.Alias)
		}
		if hp.Config == nil || len(hp.Rendered) == 0 {
			return nil, fmt.Errorf("%s has no render", hp.Alias)
		}
		t := deploy.Target{Alias: hp.Alias, ConfigPath: ref.configPath, Service: ref.service,
			Rendered: hp.Rendered, Running: ref.running, HTTPPort: DefaultHTTPPort}
		if hp.Config.HTTPPort != nil {
			t.HTTPPort = *hp.Config.HTTPPort
		}
		if hp.Config.NodeSeed != nil {
			s := *hp.Config.NodeSeed
			t.Seed = &s
			if s.Mode == stellarcfg.SeedImport && seeds != nil {
				if t.Import = seeds[hp.Alias]; t.Import == nil || t.Import.Len() == 0 {
					return nil, fmt.Errorf("the pasted seed for %s is no longer held; paste it again in the wizard", hp.Alias)
				}
			}
		}
		for _, a := range hp.Actions {
			t.Inits = append(t.Inits, deploy.Init{Kind: remote.InitKind(a.Kind), Archive: a.Archive})
		}
		out = append(out, t)
	}
	return out, nil
}

func seedText(s *stellarcfg.Seed) string {
	if s == nil {
		return "no seed"
	}
	switch s.Mode {
	case stellarcfg.SeedGenerate:
		return "new seed generated on the host"
	case stellarcfg.SeedImport:
		return "the pasted seed"
	}
	return "keep the existing seed"
}

func changedLines(diff string) int {
	n := 0
	for _, l := range strings.Split(diff, "\n") {
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") {
			n++
		}
	}
	return n
}

// openDeploy prepares the deploy of a reviewed plan and opens its screen.
func (m Model) openDeploy(msg wizard.DeployMsg) (Model, tea.Cmd) {
	zero := func() {
		for _, b := range msg.Seeds {
			b.Zero()
		}
	}
	if m.run != nil {
		zero()
		m.fleet.Flash = "A deploy is already running."
		return m, nil
	}
	hosts := map[string]hostRef{}
	for _, hp := range msg.Plan.Hosts {
		ih := m.inv.Host(hp.Alias)
		if ih == nil {
			continue
		}
		ref := hostRef{configPath: ih.ConfigPath, service: ih.Service}
		if hv := m.fleet.Host(hp.Alias); hv != nil && hv.Probe != nil {
			ref.running = hv.Probe.ServiceActive == "active"
		}
		hosts[hp.Alias] = ref
	}
	targets, err := targetsFromPlan(msg.Plan, hosts, msg.Seeds)
	if err != nil {
		zero()
		m.fleet.Flash = "Cannot deploy: " + err.Error()
		return m, nil
	}
	replan := msg.Replan
	opt := deploy.Options{Org: msg.Plan.Org, Network: msg.Plan.Network,
		Replan: func(gen map[string]string) ([]deploy.Target, error) {
			p := replan(gen)
			for _, i := range p.Issues {
				if i.Severity == quorum.Block {
					return nil, fmt.Errorf("with the new keys the plan is blocked: %s", i.String())
				}
			}
			return targetsFromPlan(p, hosts, nil)
		}}
	if m.pending != nil {
		m.pending.zero()
	}
	m.pending = &pendingDeploy{targets: targets, opt: opt}

	req := deployview.Request{Org: msg.Plan.Org, Network: msg.Plan.Network, AllowParallel: deploy.AllowParallel(msg.Plan.Network, targets)}
	for i, hp := range msg.Plan.Hosts {
		h := deployview.Host{Alias: hp.Alias, Running: targets[i].Running, New: hp.New, Seed: seedText(targets[i].Seed), Changes: changedLines(hp.Diff)}
		for _, a := range hp.Actions {
			h.Inits = append(h.Inits, strings.TrimSpace(a.Kind+" "+a.Archive))
		}
		req.Hosts = append(req.Hosts, h)
	}
	return m, m.Push(deployview.New(req))
}

// openRestore opens the deploy screen for a manual rollback to a backup.
func (m Model) openRestore(msg restoreview.RestoreMsg) (Model, tea.Cmd) {
	ih, hv := m.inv.Host(msg.Alias), m.fleet.Host(msg.Alias)
	if ih == nil || hv == nil {
		return m, nil
	}
	if m.run != nil {
		m.fleet.Flash = "A deploy is already running."
		return m, nil
	}
	t := deploy.Target{Alias: msg.Alias, ConfigPath: ih.ConfigPath, Service: ih.Service, HTTPPort: ih.HTTPPort,
		Running: hv.Probe != nil && hv.Probe.ServiceActive == "active", RestoreFrom: msg.Path, Expected: msg.Backup.Text}
	if t.HTTPPort == 0 {
		t.HTTPPort = DefaultHTTPPort
	}
	if res, err := stellarcfg.Parse(msg.Backup.Text); err == nil && res.Config.HTTPPort != nil {
		t.HTTPPort = *res.Config.HTTPPort
	}
	m.dropPending()
	m.pending = &pendingDeploy{targets: []deploy.Target{t}, opt: deploy.Options{Org: ih.Org, Network: hv.Network}}
	seed := "the backup's seed"
	if msg.Backup.PublicKey != "" {
		seed += " (" + msg.Backup.PublicKey[:8] + "…)"
	}
	return m, m.Push(deployview.New(deployview.Request{
		Title: "Restore " + msg.Alias + " from " + filepath.Base(msg.Path), Org: ih.Org, Network: hv.Network,
		Hosts: []deployview.Host{{Alias: msg.Alias, Running: t.Running, Seed: seed, Changes: msg.Changes}},
	}))
}

// handleDeploy runs the deploy screen's requests.
func (m Model) handleDeploy(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case wizard.DeployMsg:
		next, cmd := m.openDeploy(msg)
		return next, cmd, true

	case deployview.StartMsg:
		p := m.pending
		if p == nil || m.backend == nil || m.run != nil {
			return m, nil, true
		}
		m.pending = nil
		p.opt.Parallel = msg.Parallel
		ctx, cancel := context.WithCancel(context.Background())
		events := make(chan tea.Msg, 256)
		c := deploy.New(m.backend.DeployOps(), m.backend.AuditLog(), p.targets, p.opt,
			func(e deploy.Event) { events <- deployview.EventMsg{Event: e} })
		run := &deployRun{c: c, events: events, cancel: cancel}
		for _, t := range p.targets {
			run.hosts = append(run.hosts, t.Alias)
			if hv := m.fleet.Host(t.Alias); hv != nil {
				hv.Deploying = true
			}
		}
		m.run = run
		start := func() tea.Msg {
			err := c.Run(ctx)
			events <- deployview.DoneMsg{Err: err, Outcomes: c.Outcomes()}
			return nil
		}
		return m, tea.Batch(start, run.next()), true

	case deployview.EventMsg:
		cmd := m.toDeployScreen(msg)
		if m.run != nil {
			cmd = tea.Batch(cmd, m.run.next())
		}
		return m, cmd, true

	case deployview.DoneMsg:
		cmd := m.toDeployScreen(msg)
		var cmds []tea.Cmd
		if r := m.run; r != nil {
			r.cancel()
			for _, a := range r.hosts {
				hv := m.fleet.Host(a)
				if hv == nil {
					continue
				}
				hv.Deploying = false
				// Show what the host runs now.
				cmds = append(cmds, m.startProbe(hv), m.startPolling(a))
			}
			m.run = nil
		}
		return m, tea.Batch(append(cmds, cmd)...), true

	case ui.OpenAuditMsg:
		return m, m.Push(auditview.New()), true
	case auditview.LoadMsg:
		if m.backend == nil {
			return m, ui.Emit(auditview.LoadedMsg{}), true
		}
		l := m.backend.AuditLog()
		return m, func() tea.Msg {
			e, bad, err := l.Read()
			return auditview.LoadedMsg{Entries: e, Bad: bad, Path: l.Path(), Err: err}
		}, true

	case ui.OpenRestoreMsg:
		return m, m.Push(restoreview.New(msg.Alias)), true
	case restoreview.ReadBackupMsg:
		ih := m.inv.Host(msg.Alias)
		if ih == nil || m.backend == nil {
			return m, nil, true
		}
		b := m.backend
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			rc, err := b.ReadConfig(ctx, msg.Alias, msg.Path)
			return restoreview.BackupReadMsg{Alias: msg.Alias, Path: msg.Path, Config: rc, Err: err}
		}, true
	case restoreview.RestoreMsg:
		next, cmd := m.openRestore(msg)
		return next, cmd, true

	case deployview.StopWaitingMsg:
		if r := m.run; r != nil {
			alias := msg.Alias
			return m, func() tea.Msg { r.c.StopWaiting(alias); return nil }, true
		}
		return m, nil, true
	}
	return m, nil, false
}

// next waits for the deploy's next event.
func (r *deployRun) next() tea.Cmd {
	return func() tea.Msg { return <-r.events }
}

// toDeployScreen passes a deploy message to the deploy screen wherever it
// is in the stack.
func (m *Model) toDeployScreen(msg tea.Msg) tea.Cmd {
	for i, s := range m.stack {
		if dv, ok := s.(*deployview.Model); ok {
			next, cmd := dv.Update(msg)
			m.stack[i] = next
			return cmd
		}
	}
	return nil
}

// dropPending zeroes a deploy that was never started.
func (m *Model) dropPending() {
	if m.pending != nil {
		m.pending.zero()
		m.pending = nil
	}
}

var _ ui.Screen = (*deployview.Model)(nil)
