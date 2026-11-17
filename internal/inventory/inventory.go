// Package inventory is Auriga's local list of organisations and hosts
// (~/.config/auriga/inventory.toml, SPEC.md "Local state and files").
//
// It holds no secrets: public keys are informational and derived on hosts.
// Removing a host here never touches the host.
package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/sshx"
	"github.com/owenjacob-dev/auriga/internal/strkey"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// FileName is the inventory file inside the config directory.
const FileName = "inventory.toml"

// CurrentVersion is the file format version this build writes.
const CurrentVersion = 1

// Network values.
const (
	NetworkTestnet = "testnet"
	NetworkPubnet  = "pubnet"
)

// Org is an organisation: hosts sharing a NODE_HOME_DOMAIN.
type Org struct {
	Name       string `toml:"name"`
	HomeDomain string `toml:"home_domain,omitempty"`
	Network    string `toml:"network,omitempty"`
}

// Host is one validator host, identified by its ~/.ssh/config alias.
type Host struct {
	Alias         string `toml:"alias" comment:"Host alias in ~/.ssh/config"`
	Org           string `toml:"org,omitempty"`
	NodeName      string `toml:"node_name,omitempty"`
	PublicKey     string `toml:"public_key,omitempty" comment:"derived on the host, informational"`
	ConfigPath    string `toml:"config_path"`
	Service       string `toml:"service"`
	HTTPPort      int    `toml:"http_port,omitempty"`
	PublicAddress string `toml:"public_address,omitempty"`
	ArchiveName   string `toml:"archive_name,omitempty"`
	ArchiveURL    string `toml:"archive_url,omitempty"`
}

// Inventory is the whole file.
type Inventory struct {
	Version int    `toml:"version"`
	Orgs    []Org  `toml:"orgs"`
	Hosts   []Host `toml:"hosts"`
}

// New returns an empty inventory.
func New() *Inventory { return &Inventory{Version: CurrentVersion} }

// Path returns the inventory path inside configDir.
func Path(configDir string) string { return filepath.Join(configDir, FileName) }

// Load reads the inventory from configDir; a missing file is an empty
// inventory.
func Load(configDir string) (*Inventory, error) {
	b, err := os.ReadFile(Path(configDir)) //nolint:gosec // Auriga's own file
	if errors.Is(err, os.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	inv := &Inventory{}
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(inv); err != nil {
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, fmt.Errorf("%s:%d:%d: %s", Path(configDir), row, col, de.Error())
		}
		return nil, fmt.Errorf("%s: %w", Path(configDir), err)
	}
	if inv.Version != CurrentVersion {
		return nil, fmt.Errorf("%s: unsupported version %d (this build reads version %d)", Path(configDir), inv.Version, CurrentVersion)
	}
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(configDir), err)
	}
	return inv, nil
}

// Save writes the inventory to configDir atomically with mode 0600.
func (inv *Inventory) Save(configDir string) error {
	if err := inv.Validate(); err != nil {
		return err
	}
	var b bytes.Buffer
	b.WriteString("# Auriga inventory. Managed by auriga; edit with care.\n\n")
	enc := toml.NewEncoder(&b).SetIndentTables(false).SetArraysMultiline(false)
	if err := enc.Encode(inv); err != nil {
		return err
	}
	return xdg.WriteFile(Path(configDir), b.Bytes())
}

// Validate checks every field that later reaches a host or the UI.
func (inv *Inventory) Validate() error {
	orgs := map[string]bool{}
	for _, o := range inv.Orgs {
		if o.Name == "" || strings.ContainsAny(o.Name, "\n\r\t") {
			return fmt.Errorf("org name %q is invalid", o.Name)
		}
		if orgs[o.Name] {
			return fmt.Errorf("org %q is listed twice", o.Name)
		}
		orgs[o.Name] = true
		switch o.Network {
		case "", NetworkTestnet, NetworkPubnet, NetworkPrivate:
		default:
			return fmt.Errorf("org %s: network %q must be testnet or pubnet", o.Name, o.Network)
		}
	}
	seen := map[string]bool{}
	for _, h := range inv.Hosts {
		if err := sshx.ValidAlias(h.Alias); err != nil {
			return err
		}
		if seen[h.Alias] {
			return fmt.Errorf("host %q is listed twice", h.Alias)
		}
		seen[h.Alias] = true
		if h.Org != "" && !orgs[h.Org] {
			return fmt.Errorf("host %s: org %q is not defined", h.Alias, h.Org)
		}
		if err := (remote.Arg{Kind: remote.ArgPath, Value: h.ConfigPath}).Validate(); err != nil {
			return fmt.Errorf("host %s: config_path: %w", h.Alias, err)
		}
		if err := (remote.Arg{Kind: remote.ArgName, Value: h.Service}).Validate(); err != nil {
			return fmt.Errorf("host %s: service: %w", h.Alias, err)
		}
		if h.HTTPPort < 0 || h.HTTPPort > 65535 {
			return fmt.Errorf("host %s: http_port %d out of range", h.Alias, h.HTTPPort)
		}
		if h.PublicKey != "" && !strkey.ValidPublicKey(h.PublicKey) {
			return fmt.Errorf("host %s: public_key is not a valid G… key", h.Alias)
		}
	}
	return nil
}

// Host returns the host with alias, or nil.
func (inv *Inventory) Host(alias string) *Host {
	for i := range inv.Hosts {
		if inv.Hosts[i].Alias == alias {
			return &inv.Hosts[i]
		}
	}
	return nil
}

// AddHost adds alias with default paths, if absent. It reports whether the
// host was added.
func (inv *Inventory) AddHost(alias string) (bool, error) {
	if err := sshx.ValidAlias(alias); err != nil {
		return false, err
	}
	if inv.Host(alias) != nil {
		return false, nil
	}
	inv.Hosts = append(inv.Hosts, Host{
		Alias:      alias,
		ConfigPath: remote.DefaultConfigPath,
		Service:    remote.DefaultService,
	})
	return true, nil
}

// RemoveHost deletes alias from the inventory (local only). Orgs left
// without hosts are removed too.
func (inv *Inventory) RemoveHost(alias string) {
	inv.Hosts = slices.DeleteFunc(inv.Hosts, func(h Host) bool { return h.Alias == alias })
	inv.pruneOrgs()
}

func (inv *Inventory) pruneOrgs() {
	used := map[string]bool{}
	for _, h := range inv.Hosts {
		used[h.Org] = true
	}
	inv.Orgs = slices.DeleteFunc(inv.Orgs, func(o Org) bool { return !used[o.Name] })
}

// Org returns the org named name, or nil.
func (inv *Inventory) Org(name string) *Org {
	for i := range inv.Orgs {
		if inv.Orgs[i].Name == name {
			return &inv.Orgs[i]
		}
	}
	return nil
}

// GroupByHomeDomain puts alias in the org for homeDomain, creating the org
// (named after the domain) if needed. Hosts the operator assigned by hand to
// an org with a different home domain keep their assignment.
func (inv *Inventory) GroupByHomeDomain(alias, homeDomain, network string) {
	h := inv.Host(alias)
	if h == nil || homeDomain == "" {
		return
	}
	if h.Org != "" {
		if o := inv.Org(h.Org); o != nil && o.HomeDomain != "" && o.HomeDomain != homeDomain {
			return
		}
	}
	var org *Org
	for i := range inv.Orgs {
		if inv.Orgs[i].HomeDomain == homeDomain {
			org = &inv.Orgs[i]
			break
		}
	}
	if org == nil {
		inv.Orgs = append(inv.Orgs, Org{Name: homeDomain, HomeDomain: homeDomain, Network: network})
		org = &inv.Orgs[len(inv.Orgs)-1]
	}
	if org.Network == "" {
		org.Network = network
	}
	h.Org = org.Name
	inv.pruneOrgs()
}

// AssignOrg puts alias in org name, creating it if needed. An empty name
// removes the host from its org.
func (inv *Inventory) AssignOrg(alias, name string) error {
	h := inv.Host(alias)
	if h == nil {
		return fmt.Errorf("host %q not in inventory", alias)
	}
	if name != "" && inv.Org(name) == nil {
		inv.Orgs = append(inv.Orgs, Org{Name: name})
	}
	h.Org = name
	inv.pruneOrgs()
	return nil
}

// NetworkPrivate names the test-only network (the hidden
// --test-network-passphrase flag).
const NetworkPrivate = "private"

var testPassphrase string

// SetTestNetwork registers the test network's passphrase.
func SetTestNetwork(passphrase string) { testPassphrase = passphrase }

// NetworkFromPassphrase maps a NETWORK_PASSPHRASE to "testnet", "pubnet",
// "private" for the registered test network, or "".
func NetworkFromPassphrase(p string) string {
	if p != "" && p == testPassphrase {
		return NetworkPrivate
	}
	switch p {
	case "Test SDF Network ; September 2015":
		return NetworkTestnet
	case "Public Global Stellar Network ; September 2015":
		return NetworkPubnet
	}
	return ""
}
