// Package wizard is the setup wizard (SPEC.md "Setup wizard"): eight steps
// that configure an organisation's hosts, then a per-host plan for the
// deploy flow. This file and plan.go are the pure part: the answers (a
// Draft) and the plan built from them. Nothing here touches a host.
package wizard

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pelletier/go-toml/v2"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// Node types (SPEC.md "Node types").
const (
	TypeBasic    = "basic"
	TypeFull     = "full"
	TypeArchiver = "archiver"
	TypeWatcher  = "watcher"
)

// Publishes reports whether the type publishes a history archive.
func Publishes(nodeType string) bool { return nodeType == TypeFull || nodeType == TypeArchiver }

// Validates reports whether the type votes in consensus.
func Validates(nodeType string) bool { return nodeType == TypeBasic || nodeType == TypeFull }

// Draft is the wizard's answers. It is saved after every step and never
// holds a secret: an imported seed is kept in memory only, and only its
// derived public key is saved.
type Draft struct {
	Version int `toml:"version"`
	Step    int `toml:"step"`

	OrgName    string   `toml:"org_name"`
	HomeDomain string   `toml:"home_domain"`
	Hosts      []string `toml:"hosts"`

	Network string `toml:"network"` // "testnet" or "pubnet"
	// NewDBAccepted lists adopted hosts whose network changes and whose
	// operator accepted running new-db.
	NewDBAccepted []string `toml:"new_db_accepted"`

	NodeType string `toml:"node_type"`

	Keys map[string]KeyInput `toml:"keys"`

	Quorum QuorumInput `toml:"quorum"`

	Archives map[string]stellarcfg.ArchiveSpec `toml:"archives"`
	Advanced map[string]AdvancedInput          `toml:"advanced"`
}

// Seed modes for KeyInput.Mode.
const (
	KeyKeep     = "keep"
	KeyGenerate = "generate"
	KeyImport   = "import"
)

// KeyInput is step 4 for one host.
type KeyInput struct {
	Mode  string `toml:"mode"`
	Alias string `toml:"alias"`
	// Address is the public DNS name siblings put in ADDRESS,
	// e.g. "core-a.acme.org:11625".
	Address string `toml:"address"`
	// ImportedPublicKey is derived locally from a pasted seed; the seed
	// itself is never saved.
	ImportedPublicKey string `toml:"imported_public_key,omitempty"`
	// ReplaceConfirmed is set when the operator typed the alias to confirm
	// replacing a running validator's key.
	ReplaceConfirmed bool `toml:"replace_confirmed,omitempty"`
}

// QuorumInput is step 5.
type QuorumInput struct {
	OwnQuality string `toml:"own_quality"`
	UsePreset  bool   `toml:"use_preset"`
	// PresetDomains are the preset orgs to trust; empty means all.
	PresetDomains []string `toml:"preset_domains"`
	// Manual is pasted [[HOME_DOMAINS]] and [[VALIDATORS]] TOML.
	Manual string `toml:"manual"`
	// UnsafeConsent accepts a quorum set that tolerates no failures, which
	// writes UNSAFE_QUORUM=true.
	UnsafeConsent bool `toml:"unsafe_consent"`
}

// AdvancedInput is step 7 for one host.
type AdvancedInput struct {
	Database   string   `toml:"database"`
	BucketDir  string   `toml:"bucket_dir"`
	LogFile    string   `toml:"log_file"`
	HTTPPort   int      `toml:"http_port"`
	PeerPort   int      `toml:"peer_port"`
	ExtraPeers []string `toml:"extra_peers"`
	// PreferQuorum adds the quorum's public keys to PREFERRED_PEER_KEYS.
	PreferQuorum bool `toml:"prefer_quorum"`
}

// NewDraft returns a draft with defaults.
func NewDraft() *Draft {
	return &Draft{
		Version:  1,
		Network:  "testnet",
		NodeType: TypeBasic,
		Keys:     map[string]KeyInput{},
		Quorum:   QuorumInput{OwnQuality: string(stellarcfg.Medium), UsePreset: true},
		Archives: map[string]stellarcfg.ArchiveSpec{},
		Advanced: map[string]AdvancedInput{},
	}
}

var orgNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// DraftPath is where an org's draft is saved.
func DraftPath(configDir, org string) string {
	return filepath.Join(configDir, "drafts", org+".toml")
}

// SaveDraft writes the draft (owner-only).
func SaveDraft(configDir string, d *Draft) error {
	if !orgNameRE.MatchString(d.OrgName) {
		return fmt.Errorf("org name %q may only use letters, digits, '.', '_' and '-'", d.OrgName)
	}
	var b bytes.Buffer
	b.WriteString("# Auriga setup wizard draft. It holds no secrets.\n\n")
	if err := toml.NewEncoder(&b).Encode(d); err != nil {
		return err
	}
	return xdg.WriteFile(DraftPath(configDir, d.OrgName), b.Bytes())
}

// LoadDraft reads an org's draft; ok is false when there is none.
func LoadDraft(configDir, org string) (*Draft, bool, error) {
	b, err := os.ReadFile(DraftPath(configDir, org)) //nolint:gosec // Auriga's own file
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	d := NewDraft()
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(d); err != nil {
		return nil, false, fmt.Errorf("%s: %w", DraftPath(configDir, org), err)
	}
	return d, true, nil
}
