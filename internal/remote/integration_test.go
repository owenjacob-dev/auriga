//go:build integration

package remote

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/test/integration"
)

// passwordPrompter always answers with the stack's sudo password.
type passwordPrompter struct{ asked int }

func (p *passwordPrompter) SudoPassword(context.Context, string, bool) (*secret.Buffer, error) {
	p.asked++
	return secret.FromBytes([]byte(integration.SudoPassword)), nil
}

type noPrompts struct{}

func (noPrompts) ConfirmHostKey(context.Context, sshx.HostKeyQuestion) (sshx.HostKeyDecision, error) {
	return sshx.HostKeyReject, nil
}
func (noPrompts) Passphrase(context.Context, string, string, bool) (*secret.Buffer, error) {
	return nil, sshx.ErrPromptCancelled
}

// TestSecretProtocolOnRealHosts checks the stdin protocol with real sudo and
// dash: on host-a (NOPASSWD) and host-b (password via sudo -k -S).
func TestSecretProtocolOnRealHosts(t *testing.T) {
	h := integration.NewHome(t, true)
	d := &sshx.Dialer{
		Resolver:    sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath}),
		Prompter:    noPrompts{},
		Trust:       sshx.NewTrustStore(),
		Passphrases: sshx.NewPassphraseCache(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conns := map[string]Conn{}
	for _, a := range []string{"it-a", "it-b"} {
		c, err := d.Dial(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		conns[a] = c
	}
	p := &passwordPrompter{}
	r := newDirectRunner(conns, p)
	nasty := `p'a"s$(touch /tmp/PWNED)` + "`id`;\\ end"
	for _, alias := range []string{"it-a", "it-b"} {
		t.Run(alias, func(t *testing.T) {
			// Twice: the second run uses the remembered sudo mode.
			for i := range 2 {
				res, err := r.runInline(ctx, alias, `auriga_read_secret s; printf '%s|%s|%s|%s' "${#s}" "$1" "$(id -u)" "$(readlink -f /bin/sh)"`,
					[]string{"it's"}, RunOptions{Secret: secret.FromBytes([]byte(nasty))})
				if err != nil {
					t.Fatalf("run %d: %v\nstderr: %s", i, err, res.Stderr)
				}
				want := fmt.Sprintf("%d|it's|0|/usr/bin/dash", len(nasty))
				if string(res.Stdout) != want {
					t.Fatalf("run %d: stdout %q, want %q (stderr %q)", i, res.Stdout, want, res.Stderr)
				}
			}
			// A helper that never asks does not receive (or execute) it.
			res, err := r.runInline(ctx, alias, `return 0`, nil, RunOptions{Secret: secret.FromBytes([]byte("touch /tmp/EXECUTED"))})
			if err != nil {
				t.Fatalf("%v: %s", err, res.Stderr)
			}
			check, err := r.runInline(ctx, alias, `ls /tmp/EXECUTED /tmp/PWNED 2>/dev/null; true`, nil, RunOptions{})
			if err != nil || len(check.Stdout) != 0 {
				t.Errorf("secret was executed: %q %v", check.Stdout, err)
			}
		})
	}
	if p.asked != 1 {
		t.Errorf("sudo password asked %d times, want once (host-b only)", p.asked)
	}
}

// TestGenerateAndImportOnRealHosts runs gen-seed and both seed-bearing
// stage modes with the real stellar-core, sudo and dash, on a scratch
// config path so the running node is untouched.
func TestGenerateAndImportOnRealHosts(t *testing.T) {
	h := integration.NewHome(t, true)
	d := &sshx.Dialer{
		Resolver:    sshx.NewResolver(sshx.Env{Home: h.Dir, User: "op", ConfigPath: h.ConfigPath}),
		Prompter:    noPrompts{},
		Trust:       sshx.NewTrustStore(),
		Passphrases: sshx.NewPassphraseCache(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, alias := range []string{"it-a", "it-b"} {
		t.Run(alias, func(t *testing.T) {
			c, err := d.Dial(ctx, alias)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			r := newDirectRunner(map[string]Conn{alias: c}, &passwordPrompter{})
			dir := "/var/tmp/auriga-itest-seeds"
			cfg := dir + "/stellar-core.cfg"
			if _, err := r.runInline(ctx, alias, `rm -rf "$1" && mkdir -p "$1"`, []string{dir}, RunOptions{}); err != nil {
				t.Fatal(err)
			}
			defer r.runInline(context.Background(), alias, `rm -rf "$1"`, []string{dir}, RunOptions{})

			pk, err := r.GenSeed(ctx, alias, cfg)
			if err != nil || len(pk) != 56 || pk[0] != 'G' {
				t.Fatalf("gen-seed %q %v", pk, err)
			}
			if again, err := r.GenSeed(ctx, alias, cfg); err != nil || again != pk {
				t.Errorf("gen-seed again: %q %v", again, err)
			}
			render := func(mode, key string) []byte {
				return []byte("NETWORK_PASSPHRASE = \"x\"\nNODE_SEED = \"<auriga-seed:" + mode + key + "> seeds\"\n")
			}
			st, err := r.Stage(ctx, alias, cfg, render("generate", ""), SeedFromPending, nil)
			if err != nil || st.PublicKey != pk {
				t.Fatalf("stage generate: %+v %v", st, err)
			}
			rc, err := r.ReadConfig(ctx, alias, cfg+StagedSuffix)
			if err != nil || !strings.Contains(string(rc.Text), `NODE_SEED = "<redacted:`+pk+`> seeds"`) || rc.PublicKey != pk {
				t.Errorf("staged: %+v %v", rc, err)
			}
			if _, err := r.Install(ctx, alias, cfg); err != nil {
				t.Fatal(err)
			}
			if p, _ := r.Preflight(ctx, alias, cfg, DefaultService); p.PendingSeed || !p.ConfigPresent {
				t.Errorf("after install: %+v", p)
			}

			// An operator paste, over stdin only.
			paste := secret.FromBytes([]byte(integration.SeedC))
			st, err = r.Stage(ctx, alias, cfg, render("import:", integration.AddressC), SeedFromStdin, paste)
			if err != nil || st.PublicKey != integration.AddressC {
				t.Fatalf("stage import: %+v %v", st, err)
			}
			// Keep: the seed the installed config holds (the generated one).
			if st, err = r.Stage(ctx, alias, cfg, render("keep:", pk), SeedFromConfig, nil); err != nil || st.PublicKey != pk {
				t.Fatalf("stage keep: %+v %v", st, err)
			}
		})
	}
}
