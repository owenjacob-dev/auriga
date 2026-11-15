package sshx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

// AuthError explains a failed login: which keys were offered and whether an
// agent was available (SPEC.md "Errors": authentication).
type AuthError struct {
	Alias        string
	User         string
	Tried        []string
	AgentPresent bool
	Err          error
}

func (e *AuthError) Error() string {
	agentState := "no agent"
	if e.AgentPresent {
		agentState = "agent present"
	}
	tried := "no keys"
	if len(e.Tried) > 0 {
		tried = strings.Join(e.Tried, ", ")
	}
	return fmt.Sprintf("authentication as %s@%s failed (%s; tried %s); load the key into the agent or check IdentityFile", e.User, e.Alias, agentState, tried)
}

func (e *AuthError) Unwrap() error { return e.Err }

// PassphraseCache holds key passphrases for the session, keyed by key file
// path. Close zeroes them all.
type PassphraseCache struct {
	mu sync.Mutex
	m  map[string]*secret.Buffer
}

// NewPassphraseCache returns an empty cache.
func NewPassphraseCache() *PassphraseCache { return &PassphraseCache{m: map[string]*secret.Buffer{}} }

func (c *PassphraseCache) get(path string) *secret.Buffer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[path]
}

func (c *PassphraseCache) put(path string, b *secret.Buffer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.m[path]; old != nil && old != b {
		old.Zero()
	}
	c.m[path] = b
}

// Close zeroes every cached passphrase.
func (c *PassphraseCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, b := range c.m {
		b.Zero()
		delete(c.m, k)
	}
}

// authSetup is the auth configuration for one connection attempt.
type authSetup struct {
	method       ssh.AuthMethod
	agentPresent bool
	tried        *triedList
	closer       io.Closer // agent connection
}

type triedList struct {
	mu    sync.Mutex
	names []string
}

func (t *triedList) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.names {
		if n == s {
			return
		}
	}
	t.names = append(t.names, s)
}

func (t *triedList) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.names...)
}

// buildAuth offers agent keys first, then each IdentityFile, honouring
// IdentitiesOnly and -cert.pub certificates. Password and
// keyboard-interactive are never offered.
func buildAuth(ctx context.Context, alias string, h HostConfig, agentSock string, p Prompter, cache *PassphraseCache) (*authSetup, error) {
	setup := &authSetup{tried: &triedList{}}
	var agentSigners []ssh.Signer
	if agentSock != "" {
		if conn, err := (&net.Dialer{}).DialContext(ctx, "unix", agentSock); err == nil {
			ag := agent.NewClient(conn)
			if signers, err := ag.Signers(); err == nil {
				setup.agentPresent = true
				setup.closer = conn
				comments := map[string]string{}
				if keys, err := ag.List(); err == nil {
					for _, k := range keys {
						comments[string(k.Marshal())] = k.Comment
					}
				}
				for _, s := range signers {
					desc := "agent " + s.PublicKey().Type() + " " + fingerprint(s.PublicKey())
					if c := comments[string(s.PublicKey().Marshal())]; c != "" {
						desc += " (" + c + ")"
					}
					agentSigners = append(agentSigners, &recordingSigner{Signer: s, desc: desc, tried: setup.tried})
				}
			} else {
				conn.Close() //nolint:errcheck,gosec
			}
		}
	}

	var fileSigners []ssh.Signer
	var filePubs [][]byte
	for _, path := range h.IdentityFiles {
		s, pub, err := loadIdentity(ctx, alias, path, p, cache)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if setup.closer != nil {
				setup.closer.Close() //nolint:errcheck,gosec
			}
			return nil, fmt.Errorf("identity %s: %w", path, err)
		}
		filePubs = append(filePubs, pub.Marshal())
		fileSigners = append(fileSigners, &recordingSigner{Signer: s, desc: path, tried: setup.tried})
	}

	if h.IdentitiesOnly {
		kept := agentSigners[:0]
		for _, s := range agentSigners {
			// Use the inner key: recordingSigner.PublicKey marks a key as tried.
			pub := s.(*recordingSigner).Signer.PublicKey().Marshal()
			for _, fp := range filePubs {
				if bytes.Equal(pub, fp) {
					kept = append(kept, s)
					break
				}
			}
		}
		agentSigners = kept
	}
	all := append(agentSigners, fileSigners...)
	setup.method = ssh.PublicKeysCallback(func() ([]ssh.Signer, error) { return all, nil })
	return setup, nil
}

// loadIdentity returns a signer for one IdentityFile. An encrypted key whose
// public half is readable becomes a lazy signer that asks for the passphrase
// only if the server accepts the key.
func loadIdentity(ctx context.Context, alias, path string, p Prompter, cache *PassphraseCache) (ssh.Signer, ssh.PublicKey, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // operator's own key file
	if err != nil {
		return nil, nil, err
	}
	var signer ssh.Signer
	s, err := ssh.ParsePrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	switch {
	case err == nil:
		signer = s
	case errors.As(err, &missing):
		pub := missing.PublicKey
		if pub == nil {
			pub = readPublicKey(path + ".pub")
		}
		if pub == nil {
			pub = openSSHPublicKey(pem)
		}
		lazy := &lazySigner{ctx: ctx, alias: alias, path: path, pem: pem, pub: pub, prompter: p, cache: cache}
		if pub == nil {
			// No way to learn the public key without decrypting.
			if _, err := lazy.load(); err != nil {
				return nil, nil, err
			}
			lazy.pub = lazy.signer.PublicKey()
		}
		signer = lazy
	default:
		return nil, nil, err
	}
	pub := signer.PublicKey()
	if certPub := readPublicKey(path + "-cert.pub"); certPub != nil {
		if cert, ok := certPub.(*ssh.Certificate); ok {
			if cs, err := ssh.NewCertSigner(cert, signer); err == nil {
				signer = cs
			}
		}
	}
	return signer, pub, nil
}

func readPublicKey(path string) ssh.PublicKey {
	b, err := os.ReadFile(path) //nolint:gosec // operator's own key file
	if err != nil {
		return nil
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey(b)
	if err != nil {
		return nil
	}
	return k
}

// openSSHPublicKey reads the unencrypted public key from the header of an
// "openssh-key-v1" private key file.
func openSSHPublicKey(pemBytes []byte) ssh.PublicKey {
	const magic = "openssh-key-v1\x00"
	block := pemDecode(pemBytes)
	if !bytes.HasPrefix(block, []byte(magic)) {
		return nil
	}
	rest := block[len(magic):]
	readString := func() ([]byte, bool) {
		if len(rest) < 4 {
			return nil, false
		}
		n := binary.BigEndian.Uint32(rest)
		if int64(n) > int64(len(rest)-4) {
			return nil, false
		}
		s := rest[4 : 4+n]
		rest = rest[4+n:]
		return s, true
	}
	for range 3 { // ciphername, kdfname, kdfoptions
		if _, ok := readString(); !ok {
			return nil
		}
	}
	if len(rest) < 4 || binary.BigEndian.Uint32(rest) < 1 {
		return nil
	}
	rest = rest[4:]
	blob, ok := readString()
	if !ok {
		return nil
	}
	k, err := ssh.ParsePublicKey(blob)
	if err != nil {
		return nil
	}
	return k
}

// lazySigner decrypts its key on first use.
type lazySigner struct {
	ctx      context.Context
	alias    string
	path     string
	pem      []byte
	pub      ssh.PublicKey
	prompter Prompter
	cache    *PassphraseCache

	mu     sync.Mutex
	signer ssh.Signer
}

const maxPassphraseTries = 3

func (l *lazySigner) load() (ssh.Signer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.signer != nil {
		return l.signer, nil
	}
	for attempt := range maxPassphraseTries {
		pass := l.cache.get(l.path)
		if pass == nil || attempt > 0 {
			var err error
			pass, err = l.prompter.Passphrase(l.ctx, l.alias, l.path, attempt > 0)
			if err != nil {
				return nil, err
			}
		}
		s, err := ssh.ParsePrivateKeyWithPassphrase(l.pem, pass.Bytes())
		if err == nil {
			l.cache.put(l.path, pass)
			l.signer = s
			return s, nil
		}
		pass.Zero()
		if !isWrongPassphrase(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("wrong passphrase for %s", l.path)
}

func isWrongPassphrase(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "decryption password incorrect") ||
		strings.Contains(msg, "x509: decryption") ||
		strings.Contains(msg, "bcrypt_pbkdf") ||
		strings.Contains(msg, "invalid passphrase") ||
		strings.Contains(msg, "check bytes")
}

func (l *lazySigner) PublicKey() ssh.PublicKey { return l.pub }

func (l *lazySigner) Sign(rand io.Reader, data []byte) (*ssh.Signature, error) {
	s, err := l.load()
	if err != nil {
		return nil, err
	}
	return s.Sign(rand, data)
}

func (l *lazySigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	s, err := l.load()
	if err != nil {
		return nil, err
	}
	if as, ok := s.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algorithm)
	}
	return s.Sign(rand, data)
}

func (l *lazySigner) Algorithms() []string {
	if l.pub.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{l.pub.Type()}
}

// recordingSigner notes each key the client offers, for AuthError.
type recordingSigner struct {
	ssh.Signer
	desc  string
	tried *triedList
}

func (r *recordingSigner) PublicKey() ssh.PublicKey {
	r.tried.add(r.desc)
	return r.Signer.PublicKey()
}

func (r *recordingSigner) SignWithAlgorithm(rand io.Reader, data []byte, algorithm string) (*ssh.Signature, error) {
	if as, ok := r.Signer.(ssh.AlgorithmSigner); ok {
		return as.SignWithAlgorithm(rand, data, algorithm)
	}
	return r.Sign(rand, data)
}

func (r *recordingSigner) Algorithms() []string {
	if ms, ok := r.Signer.(ssh.MultiAlgorithmSigner); ok {
		return ms.Algorithms()
	}
	if r.Signer.PublicKey().Type() == ssh.KeyAlgoRSA {
		if _, ok := r.Signer.(ssh.AlgorithmSigner); ok {
			return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
		}
	}
	return []string{underlyingAlgo(r.Signer.PublicKey())}
}

func underlyingAlgo(k ssh.PublicKey) string {
	if c, ok := k.(*ssh.Certificate); ok {
		return c.Key.Type()
	}
	return k.Type()
}
