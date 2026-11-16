// Package presets holds the quorum presets the wizard offers (SPEC.md
// "Step 5: quorum set builder"): SDF's testnet validators, and the Tier 1
// organisations from SDF's example full validator config for pubnet. Both
// are embedded with their source and snapshot date. An operator-triggered
// refresh fetches the upstream file (on the operator's machine, never on a
// host), shows a diff, and saves an override in the config directory.
package presets

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

//go:embed data/*.toml
var embedded embed.FS

// Network names a preset.
type Network string

const (
	Testnet Network = "testnet"
	Pubnet  Network = "pubnet"
)

// Upstream sources the presets are extracted from.
var upstream = map[Network]string{
	Testnet: "https://raw.githubusercontent.com/stellar/stellar-core/master/docs/stellar-core_testnet_validator.cfg",
	Pubnet:  "https://raw.githubusercontent.com/stellar/packages/master/docs/examples/pubnet-validator-full/stellar-core.cfg",
}

// SourceURL is where Refresh fetches a network's preset from.
func SourceURL(n Network) string { return upstream[n] }

// Preset is the set of orgs and validators to trust on a network.
type Preset struct {
	Network     Network
	Source      string // upstream URL
	Snapshot    string // date of the upstream version, YYYY-MM-DD
	HomeDomains []stellarcfg.HomeDomain
	Validators  []stellarcfg.Validator
	// Overridden is true when the preset comes from a refresh saved in the
	// config directory rather than the embedded copy.
	Overridden bool
}

// file is the on-disk form of a preset (embedded and overrides).
type file struct {
	Source      string        `toml:"source"`
	Snapshot    string        `toml:"snapshot"`
	HomeDomains []homeDomain  `toml:"HOME_DOMAINS"`
	Validators  []validatorTb `toml:"VALIDATORS"`
}

type homeDomain struct {
	HomeDomain string `toml:"HOME_DOMAIN"`
	Quality    string `toml:"QUALITY"`
}

type validatorTb struct {
	Name       string `toml:"NAME"`
	HomeDomain string `toml:"HOME_DOMAIN"`
	PublicKey  string `toml:"PUBLIC_KEY"`
	Address    string `toml:"ADDRESS,omitempty"`
	History    string `toml:"HISTORY,omitempty"`
	Quality    string `toml:"QUALITY,omitempty"`
}

func overridePath(configDir string, n Network) string {
	return filepath.Join(configDir, "presets", string(n)+".toml")
}

// Load returns n's preset: the saved override if there is one, else the
// embedded snapshot.
func Load(configDir string, n Network) (*Preset, error) {
	if _, ok := upstream[n]; !ok {
		return nil, fmt.Errorf("no preset for network %q", n)
	}
	if configDir != "" {
		b, err := os.ReadFile(overridePath(configDir, n)) //nolint:gosec // Auriga's own file
		switch {
		case err == nil:
			p, err := decode(n, b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", overridePath(configDir, n), err)
			}
			p.Overridden = true
			return p, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
	}
	b, err := embedded.ReadFile("data/" + string(n) + ".toml")
	if err != nil {
		return nil, err
	}
	return decode(n, b)
}

func decode(n Network, b []byte) (*Preset, error) {
	var f file
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&f); err != nil {
		return nil, err
	}
	p := &Preset{Network: n, Source: f.Source, Snapshot: f.Snapshot}
	for _, h := range f.HomeDomains {
		p.HomeDomains = append(p.HomeDomains, stellarcfg.HomeDomain{HomeDomain: h.HomeDomain, Quality: stellarcfg.Quality(h.Quality)})
	}
	for _, v := range f.Validators {
		p.Validators = append(p.Validators, stellarcfg.Validator{Name: v.Name, HomeDomain: v.HomeDomain, PublicKey: v.PublicKey,
			Address: v.Address, History: v.History, Quality: stellarcfg.Quality(v.Quality)})
	}
	return p, p.validate()
}

// validate checks the preset with the same rules as a config.
func (p *Preset) validate() error {
	c := &stellarcfg.Config{HomeDomains: p.HomeDomains, Validators: p.Validators}
	var bad []string
	for _, i := range stellarcfg.Blocking(stellarcfg.Validate(c, stellarcfg.ValidateOptions{})) {
		if i.Rule == "network-passphrase" || i.Rule == "database-missing" {
			continue
		}
		bad = append(bad, i.String())
	}
	if len(p.Validators) == 0 {
		bad = append(bad, "no validators")
	}
	if len(bad) > 0 {
		return fmt.Errorf("preset is invalid: %s", strings.Join(bad, "; "))
	}
	return nil
}

// Extract builds a preset from an upstream stellar-core config: the
// [[HOME_DOMAINS]] that have validators, and all [[VALIDATORS]]. Nothing
// else is read, so a seed in the file is never touched.
func Extract(n Network, source, snapshot string, upstreamCfg []byte) (*Preset, error) {
	var f struct {
		HomeDomains []homeDomain  `toml:"HOME_DOMAINS"`
		Validators  []validatorTb `toml:"VALIDATORS"`
	}
	if err := toml.Unmarshal(upstreamCfg, &f); err != nil {
		return nil, fmt.Errorf("upstream config does not parse: %w", err)
	}
	used := map[string]bool{}
	for _, v := range f.Validators {
		used[v.HomeDomain] = true
	}
	out := file{Source: source, Snapshot: snapshot, Validators: f.Validators}
	for _, h := range f.HomeDomains {
		if used[h.HomeDomain] {
			out.HomeDomains = append(out.HomeDomains, h)
		}
	}
	b, err := encode(out)
	if err != nil {
		return nil, err
	}
	return decode(n, b)
}

func encode(f file) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Auriga quorum preset, extracted from %s\n", f.Source)
	fmt.Fprintf(&b, "# Only [[HOME_DOMAINS]] and [[VALIDATORS]] are kept.\n\n")
	if err := toml.NewEncoder(&b).SetIndentTables(false).Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Encode returns the preset's on-disk form.
func (p *Preset) Encode() ([]byte, error) {
	f := file{Source: p.Source, Snapshot: p.Snapshot}
	for _, h := range p.HomeDomains {
		f.HomeDomains = append(f.HomeDomains, homeDomain{HomeDomain: h.HomeDomain, Quality: string(h.Quality)})
	}
	for _, v := range p.Validators {
		f.Validators = append(f.Validators, validatorTb{Name: v.Name, HomeDomain: v.HomeDomain, PublicKey: v.PublicKey,
			Address: v.Address, History: v.History, Quality: string(v.Quality)})
	}
	return encode(f)
}

// SaveOverride writes p to the config directory, replacing the embedded
// snapshot for its network.
func SaveOverride(configDir string, p *Preset) error {
	b, err := p.Encode()
	if err != nil {
		return err
	}
	return xdg.WriteFile(overridePath(configDir, p.Network), b)
}

// Summary describes a preset in one line.
func (p *Preset) Summary() string {
	src := "built in"
	if p.Overridden {
		src = "refreshed"
	}
	orgs := "orgs"
	if len(p.HomeDomains) == 1 {
		orgs = "org"
	}
	return fmt.Sprintf("%d %s, %d validators (%s, snapshot %s)", len(p.HomeDomains), orgs, len(p.Validators), src, p.Snapshot)
}
