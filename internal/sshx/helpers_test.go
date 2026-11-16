package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

// hostSpec is what the fake "ssh -G" prints for one alias.
type hostSpec struct {
	addr      string // host:port
	user      string
	identity  []string
	idOnly    bool
	proxyJump string
	strict    string
	timeout   int // seconds
}

// fakeResolver answers ssh -G from a table, so tests control every alias.
func fakeResolver(t *testing.T, home string, hosts map[string]hostSpec) *Resolver {
	t.Helper()
	env := Env{Home: home, User: "op", SSHPath: "/fake/ssh"}
	env.Run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		alias := args[len(args)-1]
		h, ok := hosts[alias]
		if !ok {
			return nil, fmt.Errorf("no such alias %q", alias)
		}
		host, port, _ := net.SplitHostPort(h.addr)
		var b strings.Builder
		fmt.Fprintf(&b, "hostname %s\nport %s\nuser %s\n", host, port, orDefault(h.user, "op"))
		for _, id := range h.identity {
			fmt.Fprintf(&b, "identityfile %s\n", id)
		}
		if len(h.identity) == 0 {
			fmt.Fprintf(&b, "identityfile %s\n", filepath.Join(home, ".ssh", "none"))
		}
		if h.idOnly {
			b.WriteString("identitiesonly yes\n")
		}
		if h.proxyJump != "" {
			fmt.Fprintf(&b, "proxyjump %s\n", h.proxyJump)
		}
		fmt.Fprintf(&b, "stricthostkeychecking %s\n", orDefault(h.strict, "ask"))
		fmt.Fprintf(&b, "userknownhostsfile %s\n", filepath.Join(home, ".ssh", "known_hosts"))
		b.WriteString("globalknownhostsfile /nonexistent/ssh_known_hosts\n")
		if h.timeout > 0 {
			fmt.Fprintf(&b, "connecttimeout %d\n", h.timeout)
		}
		return []byte(b.String()), nil
	}
	return NewResolver(env)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// scriptedPrompter answers prompts from queues and records the questions.
type scriptedPrompter struct {
	mu          sync.Mutex
	decisions   []HostKeyDecision
	passphrases []string
	delay       time.Duration
	hostKeyQs   []HostKeyQuestion
	passQs      []string
}

func (p *scriptedPrompter) ConfirmHostKey(_ context.Context, q HostKeyQuestion) (HostKeyDecision, error) {
	time.Sleep(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hostKeyQs = append(p.hostKeyQs, q)
	if len(p.decisions) == 0 {
		return HostKeyReject, nil
	}
	d := p.decisions[0]
	p.decisions = p.decisions[1:]
	return d, nil
}

func (p *scriptedPrompter) Passphrase(_ context.Context, alias, path string, retry bool) (*secret.Buffer, error) {
	time.Sleep(p.delay)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.passQs = append(p.passQs, fmt.Sprintf("%s %s retry=%v", alias, filepath.Base(path), retry))
	if len(p.passphrases) == 0 {
		return nil, fmt.Errorf("no passphrase scripted")
	}
	pw := p.passphrases[0]
	p.passphrases = p.passphrases[1:]
	return secret.FromBytes([]byte(pw)), nil
}

func (p *scriptedPrompter) questions() ([]HostKeyQuestion, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]HostKeyQuestion(nil), p.hostKeyQs...), append([]string(nil), p.passQs...)
}

// newKey returns an ed25519 private key and its signer.
func newKey(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, s
}

// writeKey writes an OpenSSH private key file, encrypted when passphrase is
// non-empty, and its .pub unless skipPub.
func writeKey(t *testing.T, path string, priv ed25519.PrivateKey, passphrase string, skipPub bool) {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "test", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "test")
	}
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(pem.EncodeToMemory(block)))
	if !skipPub {
		pub, _ := ssh.NewPublicKey(priv.Public())
		writeFile(t, path+".pub", string(ssh.MarshalAuthorizedKey(pub)))
	}
}

// trustHost appends srv's host key to home's known_hosts.
func trustHost(t *testing.T, home string, srv *sshtest.Server) {
	t.Helper()
	f := filepath.Join(home, ".ssh", "known_hosts")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		t.Fatal(err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(srv.Addr)}, srv.HostKey.PublicKey()) + "\n"
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// startAgent serves a keyring on a unix socket and returns its path.
func startAgent(t *testing.T, keys ...ed25519.PrivateKey) string {
	t.Helper()
	kr := agent.NewKeyring()
	for _, k := range keys {
		if err := kr.Add(agent.AddedKey{PrivateKey: k, Comment: "agent-test-key"}); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "agent")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go agent.ServeAgent(kr, c) //nolint:errcheck
		}
	}()
	return sock
}

func newDialer(r *Resolver, p Prompter, agentSock string) *Dialer {
	return &Dialer{Resolver: r, Prompter: p, Trust: NewTrustStore(), Passphrases: NewPassphraseCache(), AgentSock: agentSock}
}

func runRemote(t *testing.T, c *ssh.Client, cmd string) string {
	t.Helper()
	sess, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	out, err := sess.Output(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
