package sshx

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/sshx/sshtest"
)

func execEcho(cmd string, _ io.Reader, stdout, _ io.Writer) int {
	io.WriteString(stdout, "ran "+cmd) //nolint:errcheck
	return 0
}

type fixture struct {
	home string
	srv  *sshtest.Server
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	srv := sshtest.NewServer(t)
	srv.Exec = execEcho
	srv.Start()
	return fixture{home: t.TempDir(), srv: srv}
}

func (f fixture) key(t *testing.T, name, passphrase string, authorize bool) string {
	t.Helper()
	priv, s := newKey(t)
	path := filepath.Join(f.home, ".ssh", name)
	writeKey(t, path, priv, passphrase, false)
	if authorize {
		f.srv.AuthorizedKeys = append(f.srv.AuthorizedKeys, s.PublicKey())
	}
	return path
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestDialWithKeyFile(t *testing.T) {
	f := newFixture(t)
	key := f.key(t, "id_ed25519", "", true)
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"acme-a": {addr: f.srv.Addr, user: "ops", identity: []string{key}}})
	p := &scriptedPrompter{}
	conn, err := newDialer(r, p, "").Dial(ctx(t), "acme-a")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := runRemote(t, conn.Client, "true"); got != "ran true" {
		t.Errorf("exec output %q", got)
	}
	if hk, pq := p.questions(); len(hk)+len(pq) != 0 {
		t.Errorf("unexpected prompts: %v %v", hk, pq)
	}
	if !slices.Equal(f.srv.Logins(), []string{"ops"}) {
		t.Errorf("logins %v", f.srv.Logins())
	}
}

func TestDialWithAgentFirst(t *testing.T) {
	f := newFixture(t)
	priv, s := newKey(t)
	f.srv.AuthorizedKeys = append(f.srv.AuthorizedKeys, s.PublicKey())
	// An encrypted file key that would prompt if it were tried first.
	enc := f.key(t, "id_enc", "pw", false)
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{enc}}})
	p := &scriptedPrompter{}
	conn, err := newDialer(r, p, startAgent(t, priv)).Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, pq := p.questions(); len(pq) != 0 {
		t.Errorf("passphrase prompted although the agent key worked: %v", pq)
	}
}

func TestIdentitiesOnlyFiltersAgent(t *testing.T) {
	f := newFixture(t)
	priv, s := newKey(t)
	f.srv.AuthorizedKeys = append(f.srv.AuthorizedKeys, s.PublicKey())
	other := f.key(t, "id_other", "", false)
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{other}, idOnly: true}})
	_, err := newDialer(r, &scriptedPrompter{}, startAgent(t, priv)).Dial(ctx(t), "a")
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want AuthError", err)
	}
	if !ae.AgentPresent || !slices.Equal(ae.Tried, []string{other}) {
		t.Errorf("AuthError %+v", ae)
	}
	if !strings.Contains(ae.Error(), "agent present") || !strings.Contains(ae.Error(), "id_other") {
		t.Errorf("message %q", ae.Error())
	}
}

func TestAuthErrorListsKeys(t *testing.T) {
	f := newFixture(t)
	k1 := f.key(t, "id_one", "", false)
	k2 := f.key(t, "id_two", "", false)
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{k1, filepath.Join(f.home, ".ssh", "missing"), k2}}})
	_, err := newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "a")
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v", err)
	}
	if ae.AgentPresent || !slices.Equal(ae.Tried, []string{k1, k2}) {
		t.Errorf("AuthError %+v", ae)
	}
}

func TestCertificateAuth(t *testing.T) {
	f := newFixture(t)
	ca := sshtest.NewSigner(t)
	f.srv.CertAuthority = ca.PublicKey()
	priv, s := newKey(t)
	path := filepath.Join(f.home, ".ssh", "id_cert")
	writeKey(t, path, priv, "", false)
	cert := &ssh.Certificate{
		Key: s.PublicKey(), CertType: ssh.UserCert, KeyId: "op",
		ValidPrincipals: []string{"op"}, ValidBefore: ssh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path+"-cert.pub", string(ssh.MarshalAuthorizedKey(cert)))
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{path}}})
	conn, err := newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestEncryptedKeyPromptsLazilyAndCaches(t *testing.T) {
	f := newFixture(t)
	unauth := f.key(t, "id_unauth_enc", "nope", false) // offered first, rejected without a prompt
	key := f.key(t, "id_enc", "correct horse", true)
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{unauth, key}}})
	p := &scriptedPrompter{passphrases: []string{"wrong", "correct horse"}}
	d := newDialer(r, p, "")
	conn, err := d.Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	_, pq := p.questions()
	if want := []string{"a id_enc retry=false", "a id_enc retry=true"}; !slices.Equal(pq, want) {
		t.Errorf("prompts %v, want %v", pq, want)
	}
	// Second connection reuses the cached passphrase.
	conn, err = d.Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, pq := p.questions(); len(pq) != 2 {
		t.Errorf("prompted again: %v", pq)
	}
	d.Passphrases.Close()
	if d.Passphrases.get(key) != nil {
		t.Error("cache not cleared")
	}
}

func TestEncryptedKeyWithoutPubFile(t *testing.T) {
	f := newFixture(t)
	priv, s := newKey(t)
	f.srv.AuthorizedKeys = append(f.srv.AuthorizedKeys, s.PublicKey())
	path := filepath.Join(f.home, ".ssh", "id_nopub")
	writeKey(t, path, priv, "pw", true)
	if got := openSSHPublicKey(mustRead(t, path)); got == nil || string(got.Marshal()) != string(s.PublicKey().Marshal()) {
		t.Fatal("public key not read from the OpenSSH header")
	}
	trustHost(t, f.home, f.srv)
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{path}}})
	conn, err := newDialer(r, &scriptedPrompter{passphrases: []string{"pw"}}, "").Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnknownHostKeyDecisions(t *testing.T) {
	f := newFixture(t)
	key := f.key(t, "id", "", true)
	spec := map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{key}}}
	knownHosts := filepath.Join(f.home, ".ssh", "known_hosts")

	t.Run("reject", func(t *testing.T) {
		p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyReject}}
		_, err := newDialer(fakeResolver(t, f.home, spec), p, "").Dial(ctx(t), "a")
		var re *HostKeyRejectedError
		if !errors.As(err, &re) || re.Strict {
			t.Fatalf("err = %v", err)
		}
		qs, _ := p.questions()
		if len(qs) != 1 || qs[0].Fingerprint != ssh.FingerprintSHA256(f.srv.HostKey.PublicKey()) || qs[0].KeyType != "ssh-ed25519" || qs[0].SaveTo != knownHosts {
			t.Errorf("question %+v", qs)
		}
	})
	t.Run("trust for session", func(t *testing.T) {
		p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyTrustSession}}
		d := newDialer(fakeResolver(t, f.home, spec), p, "")
		for range 2 {
			conn, err := d.Dial(ctx(t), "a")
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
		}
		if qs, _ := p.questions(); len(qs) != 1 {
			t.Errorf("asked %d times, want once per session", len(qs))
		}
		if _, err := os.Stat(knownHosts); err == nil {
			t.Error("session trust wrote known_hosts")
		}
	})
	t.Run("trust and save", func(t *testing.T) {
		p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyTrustAndSave}}
		conn, err := newDialer(fakeResolver(t, f.home, spec), p, "").Dial(ctx(t), "a")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		fi, err := os.Stat(knownHosts)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("known_hosts: %v %v", fi, err)
		}
		// A fresh session (new trust store) now connects without asking.
		p2 := &scriptedPrompter{}
		conn, err = newDialer(fakeResolver(t, f.home, spec), p2, "").Dial(ctx(t), "a")
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if qs, _ := p2.questions(); len(qs) != 0 {
			t.Errorf("asked again after saving: %v", qs)
		}
	})
}

func TestStrictHostKeyCheckingYes(t *testing.T) {
	f := newFixture(t)
	key := f.key(t, "id", "", true)
	p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyTrustSession}}
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{key}, strict: "true"}})
	_, err := newDialer(r, p, "").Dial(ctx(t), "a")
	var re *HostKeyRejectedError
	if !errors.As(err, &re) || !re.Strict {
		t.Fatalf("err = %v", err)
	}
	if qs, _ := p.questions(); len(qs) != 0 {
		t.Error("prompted despite StrictHostKeyChecking yes")
	}
}

func TestChangedHostKeyIsHardFailure(t *testing.T) {
	f := newFixture(t)
	key := f.key(t, "id", "", true)
	trustHost(t, f.home, f.srv)
	oldFP := ssh.FingerprintSHA256(f.srv.HostKey.PublicKey())
	// The host now presents a different key of the same type.
	f.srv.Stop()
	f.srv.HostKey = sshtest.NewSigner(t)
	f.srv.Start()
	p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyTrustAndSave}}
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{key}}})
	_, err := newDialer(r, p, "").Dial(ctx(t), "a")
	var ce *HostKeyChangedError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	newFP := ssh.FingerprintSHA256(f.srv.HostKey.PublicKey())
	if !strings.Contains(ce.Error(), oldFP) || !strings.Contains(ce.Error(), newFP) {
		t.Errorf("message lacks both fingerprints: %s", ce.Error())
	}
	if qs, _ := p.questions(); len(qs) != 0 {
		t.Error("changed key offered a prompt")
	}
}

func TestTwoHopProxyJump(t *testing.T) {
	home := t.TempDir()
	priv, s := newKey(t)
	path := filepath.Join(home, ".ssh", "id")
	writeKey(t, path, priv, "", false)
	var servers []*sshtest.Server
	for range 3 {
		srv := sshtest.NewServer(t)
		srv.AuthorizedKeys = []ssh.PublicKey{s.PublicKey()}
		srv.AllowForwarding = true
		srv.Exec = execEcho
		srv.Start()
		trustHost(t, home, srv)
		servers = append(servers, srv)
	}
	j1, j2, target := servers[0], servers[1], servers[2]
	id := []string{path}

	t.Run("explicit chain", func(t *testing.T) {
		r := fakeResolver(t, home, map[string]hostSpec{
			"jump1":  {addr: j1.Addr, user: "j1", identity: id},
			"jump2":  {addr: j2.Addr, user: "j2", identity: id, proxyJump: "should-be-ignored"},
			"target": {addr: target.Addr, user: "ops", identity: id, proxyJump: "jump1,jump2"},
		})
		conn, err := newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "target")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if !slices.Equal(conn.Route, []string{"jump1", "jump2", "target"}) {
			t.Errorf("route %v", conn.Route)
		}
		if got := runRemote(t, conn.Client, "hostname"); got != "ran hostname" {
			t.Errorf("exec through jumps: %q", got)
		}
	})
	t.Run("recursive config", func(t *testing.T) {
		r := fakeResolver(t, home, map[string]hostSpec{
			"jump1":  {addr: j1.Addr, identity: id},
			"jump2":  {addr: j2.Addr, identity: id, proxyJump: "jump1"},
			"target": {addr: target.Addr, identity: id, proxyJump: "jump2"},
		})
		conn, err := newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "target")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if !slices.Equal(conn.Route, []string{"jump1", "jump2", "target"}) {
			t.Errorf("route %v", conn.Route)
		}
	})
	t.Run("loop detected", func(t *testing.T) {
		r := fakeResolver(t, home, map[string]hostSpec{
			"x": {addr: j1.Addr, identity: id, proxyJump: "y"},
			"y": {addr: j2.Addr, identity: id, proxyJump: "x"},
		})
		_, err := newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "x")
		if err == nil || !strings.Contains(err.Error(), "deeper") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestHandshakeTimeout(t *testing.T) {
	// A server that accepts TCP but never speaks SSH.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	home := t.TempDir()
	r := fakeResolver(t, home, map[string]hostSpec{"silent": {addr: ln.Addr().String(), timeout: 1}})
	start := time.Now()
	_, err = newDialer(r, &scriptedPrompter{}, "").Dial(ctx(t), "silent")
	var ce *ConnectError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want ConnectError", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("took %v", el)
	}
}

func TestPromptTimeDoesNotCountAgainstTimeout(t *testing.T) {
	f := newFixture(t)
	key := f.key(t, "id", "", true)
	// Operator takes 1.5s to answer; ConnectTimeout is 1s.
	p := &scriptedPrompter{decisions: []HostKeyDecision{HostKeyTrustSession}, delay: 1500 * time.Millisecond}
	r := fakeResolver(t, f.home, map[string]hostSpec{"a": {addr: f.srv.Addr, identity: []string{key}, timeout: 1}})
	conn, err := newDialer(r, p, "").Dial(ctx(t), "a")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
