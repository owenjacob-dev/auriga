package secrets

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui"
	"github.com/owenjacob-dev/auriga/internal/ui/theme"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

// wizardDriver runs a wizard screen and performs its I/O requests with the
// real implementations (draft files, embedded presets).
type wizardDriver struct {
	t         *testing.T
	w         ui.Screen
	configDir string
	frames    []string
	ctx       ui.Context
}

func (d *wizardDriver) send(msg tea.Msg) {
	d.t.Helper()
	next, cmd := d.w.Update(msg)
	d.w = next
	d.run(cmd)
	l := d.w.View(d.ctx)
	d.frames = append(d.frames, strings.Join(l.Body, "\n")+l.Status)
}

func (d *wizardDriver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(20 * time.Millisecond):
		return
	}
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
	case wizard.LoadMsg:
		d.send(wizard.LoadedMsg{})
	case wizard.LoadPresetMsg:
		p, err := presets.Load(d.configDir, msg.Network)
		d.send(wizard.PresetMsg{Preset: p, Err: err})
	case wizard.SaveDraftMsg:
		if err := wizard.SaveDraft(d.configDir, msg.Draft); err != nil {
			d.t.Fatal(err)
		}
	default:
		d.send(msg)
	}
}

func (d *wizardDriver) keys(ks ...string) {
	for _, k := range ks {
		switch k {
		case "enter":
			d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "down":
			d.send(tea.KeyPressMsg{Code: tea.KeyDown})
		case "esc":
			d.send(tea.KeyPressMsg{Code: tea.KeyEscape})
		default:
			for _, r := range k {
				d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
		}
	}
}

// TestWizardImportDoesNotLeak imports a seed in the real wizard, saves the
// draft through the real code several times, and scans the files and every
// frame drawn.
func TestWizardImportDoesNotLeak(t *testing.T) {
	dirs := env(t)
	fleet := &ui.Fleet{}
	for _, h := range []string{"canary-a"} {
		cl := stellarcfg.Classify(false, nil)
		fleet.Hosts = append(fleet.Hosts, &ui.HostView{Alias: h, Class: &cl,
			Probe: &remote.ProbeFacts{ServiceActive: "inactive", ServiceWorkDir: "/"}})
	}
	w := wizard.New("canary", []string{"canary-a"}, "test", time.Now)
	w.SetFleet(fleet)
	d := &wizardDriver{t: t, w: w, configDir: dirs.Config,
		ctx: ui.Context{Theme: theme.New(true, true), Cols: 76, Rows: 20, Now: time.Now(), Fleet: fleet}}
	d.run(w.Init())

	d.keys("enter", "canary.example", "enter", "enter") // scope
	d.keys("enter", "enter")                            // network, type
	d.keys("down", "enter", "enter", "enter", "enter")  // keys: import
	d.send(tea.PasteMsg{Content: hostSeed})
	d.keys("enter")
	d.keys("esc", "esc", "esc") // back: each step saves the draft again

	b, err := os.ReadFile(wizard.DraftPath(dirs.Config, "canary"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H") {
		t.Fatal("the draft lacks the imported public key; the import did not happen")
	}
	w.Close()
	planted = append(planted, hostSeed)
	t.Cleanup(func() { planted = planted[:2] })
	scan(t, []string{dirs.Config, dirs.State, os.Getenv("HOME")}, []byte(strings.Join(d.frames, "\n")))
}
