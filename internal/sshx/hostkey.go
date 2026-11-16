package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyChangedError is a hard failure: the presented key differs from the
// one in known_hosts. There is no override (SPEC.md "Host key verification").
type HostKeyChangedError struct {
	Alias, Address string
	Presented      string   // type and fingerprint of the presented key
	Known          []string // "type fingerprint (file:line)" for each known key
}

func (e *HostKeyChangedError) Error() string {
	return fmt.Sprintf("host key for %s (%s) has CHANGED: presented %s, known_hosts has %v; verify the new key, then fix known_hosts yourself",
		e.Alias, e.Address, e.Presented, e.Known)
}

// HostKeyRejectedError means an unknown key was not accepted, either by the
// operator or because StrictHostKeyChecking is yes.
type HostKeyRejectedError struct {
	Alias, Address string
	Presented      string
	Strict         bool
}

func (e *HostKeyRejectedError) Error() string {
	if e.Strict {
		return fmt.Sprintf("host key for %s (%s) is not in known_hosts and StrictHostKeyChecking is yes (presented %s)", e.Alias, e.Address, e.Presented)
	}
	return fmt.Sprintf("host key for %s (%s) rejected (presented %s)", e.Alias, e.Address, e.Presented)
}

// TrustStore holds keys trusted for this session only. It is shared by all
// workers and never written to disk.
type TrustStore struct {
	mu   sync.Mutex
	keys map[string][]ssh.PublicKey // normalized address -> keys
}

// NewTrustStore returns an empty store.
func NewTrustStore() *TrustStore { return &TrustStore{keys: map[string][]ssh.PublicKey{}} }

func (t *TrustStore) add(addr string, k ssh.PublicKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := knownhosts.Normalize(addr)
	t.keys[a] = append(t.keys[a], k)
}

func (t *TrustStore) has(addr string, k ssh.PublicKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, known := range t.keys[knownhosts.Normalize(addr)] {
		if string(known.Marshal()) == string(k.Marshal()) {
			return true
		}
	}
	return false
}

// knownHostsFiles returns the configured files that exist.
func knownHostsFiles(h HostConfig) []string {
	var out []string
	for _, f := range append(append([]string{}, h.UserKnownHostsFiles...), h.GlobalKnownHostsFiles...) {
		if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() {
			out = append(out, f)
		}
	}
	return out
}

// hostKeyVerifier builds the callback for one hop. It also returns the host
// key algorithms to offer, so the server presents a key type we already know.
type hostKeyVerifier struct {
	ctx      context.Context
	alias    string
	h        HostConfig
	trust    *TrustStore
	prompter Prompter
	db       ssh.HostKeyCallback // nil when no known_hosts file exists
}

func newHostKeyVerifier(ctx context.Context, alias string, h HostConfig, trust *TrustStore, p Prompter) (*hostKeyVerifier, error) {
	v := &hostKeyVerifier{ctx: ctx, alias: alias, h: h, trust: trust, prompter: p}
	if files := knownHostsFiles(h); len(files) > 0 {
		db, err := knownhosts.New(files...)
		if err != nil {
			return nil, fmt.Errorf("read known_hosts: %w", err)
		}
		v.db = db
	}
	return v, nil
}

// algorithms lists host key algorithms for the key types known for addr, or
// nil to use the library default.
func (v *hostKeyVerifier) algorithms(addr string) []string {
	if v.db == nil {
		return nil
	}
	err := v.db(addr, &net.TCPAddr{IP: net.IPv4zero}, probeKey{})
	var ke *knownhosts.KeyError
	if !errors.As(err, &ke) || len(ke.Want) == 0 {
		return nil
	}
	var algos []string
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			algos = append(algos, a)
		}
	}
	for _, k := range ke.Want {
		switch t := k.Key.Type(); t {
		case ssh.KeyAlgoRSA:
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
			add(ssh.KeyAlgoRSA)
		default:
			add(t)
		}
	}
	return algos
}

func (v *hostKeyVerifier) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	presented := key.Type() + " " + fingerprint(key)
	if v.db != nil {
		err := v.db(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		var re *knownhosts.RevokedError
		switch {
		case errors.As(err, &re):
			return fmt.Errorf("host key for %s is revoked in %s:%d", v.alias, re.Revoked.Filename, re.Revoked.Line)
		case errors.As(err, &ke) && len(ke.Want) > 0:
			known := make([]string, 0, len(ke.Want))
			for _, w := range ke.Want {
				known = append(known, fmt.Sprintf("%s %s (%s:%d)", w.Key.Type(), fingerprint(w.Key), w.Filename, w.Line))
			}
			return &HostKeyChangedError{Alias: v.alias, Address: hostname, Presented: presented, Known: known}
		case errors.As(err, &ke):
			// unknown host: fall through
		default:
			return err
		}
	}
	if v.trust.has(hostname, key) {
		return nil
	}
	if v.h.StrictHostKeyChecking == "yes" {
		return &HostKeyRejectedError{Alias: v.alias, Address: hostname, Presented: presented, Strict: true}
	}
	q := HostKeyQuestion{
		Alias: v.alias, Address: hostname, KeyType: key.Type(), Fingerprint: fingerprint(key),
		SaveTo: v.saveFile(),
	}
	decision, err := v.prompter.ConfirmHostKey(v.ctx, q)
	if err != nil {
		return err
	}
	switch decision {
	case HostKeyTrustSession:
		v.trust.add(hostname, key)
		return nil
	case HostKeyTrustAndSave:
		if err := appendKnownHost(q.SaveTo, hostname, key); err != nil {
			return fmt.Errorf("save host key: %w", err)
		}
		v.trust.add(hostname, key)
		return nil
	default:
		return &HostKeyRejectedError{Alias: v.alias, Address: hostname, Presented: presented}
	}
}

// saveFile is the first UserKnownHostsFile, normally ~/.ssh/known_hosts.
func (v *hostKeyVerifier) saveFile() string {
	if len(v.h.UserKnownHostsFiles) > 0 {
		return v.h.UserKnownHostsFiles[0]
	}
	return ""
}

// appendKnownHost appends one line in OpenSSH format. The file and its
// directory are created owner-only if missing.
func appendKnownHost(file, addr string, key ssh.PublicKey) error {
	if file == "" {
		return errors.New("no UserKnownHostsFile configured")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // operator's known_hosts
	if err != nil {
		return err
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key) + "\n"
	if _, err := f.WriteString(line); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	return f.Close()
}

// probeKey is a key no host has; checking it against known_hosts reveals
// which keys are known for an address.
type probeKey struct{}

func (probeKey) Type() string                        { return "auriga-probe" }
func (probeKey) Marshal() []byte                     { return []byte("auriga-probe") }
func (probeKey) Verify([]byte, *ssh.Signature) error { return errors.New("probe key") }
