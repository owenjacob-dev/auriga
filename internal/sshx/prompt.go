package sshx

import (
	"context"

	"golang.org/x/crypto/ssh"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

// HostKeyDecision is the operator's answer to an unknown host key.
type HostKeyDecision int

const (
	// HostKeyReject refuses the connection.
	HostKeyReject HostKeyDecision = iota
	// HostKeyTrustSession accepts the key until Auriga exits.
	HostKeyTrustSession
	// HostKeyTrustAndSave accepts the key and appends it to ~/.ssh/known_hosts.
	HostKeyTrustAndSave
)

// HostKeyQuestion describes an unknown host key for the operator.
type HostKeyQuestion struct {
	Alias       string
	Address     string // host:port as checked against known_hosts
	KeyType     string // e.g. "ssh-ed25519"
	Fingerprint string // SHA256:…
	SaveTo      string // the known_hosts file "trust and save" appends to
}

// Prompter asks the operator questions on behalf of a host worker. Calls
// block the worker goroutine (never the UI) until the operator answers or
// ctx ends.
type Prompter interface {
	ConfirmHostKey(ctx context.Context, q HostKeyQuestion) (HostKeyDecision, error)
	// Passphrase asks for an encrypted key's passphrase. The caller owns the
	// returned buffer.
	Passphrase(ctx context.Context, alias, keyPath string, retry bool) (*secret.Buffer, error)
}

// fingerprint is the OpenSSH SHA256 fingerprint of a key.
func fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }
