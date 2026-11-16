//go:build integration

package remote

import (
	"context"
	"fmt"
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
