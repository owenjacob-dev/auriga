package wizard

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/quorum"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/strkey"
)

// Steps of the wizard, 1-based as shown to the operator.
const (
	StepScope = iota + 1
	StepNetwork
	StepType
	StepKeys
	StepQuorum
	StepHistory
	StepAdvanced
	StepReview
	NumSteps = StepReview
)

// StepNames label the step bar.
var StepNames = [...]string{"", "Scope", "Network", "Node type", "Keys", "Quorum", "History", "Advanced", "Review"}

// Defaults for new nodes: absolute paths, because the packaged unit runs
// from / (docs/decisions.md, M2).
const (
	DefaultBucketDir = "/var/lib/stellar/buckets"
	DefaultLogFile   = "/var/log/stellar/stellar-core.log"
	DefaultHTTPPort  = 11626
	DefaultPeerPort  = 11625
)

// Passphrases of the supported networks.
var Passphrases = map[string]string{
	inventory.NetworkTestnet: "Test SDF Network ; September 2015",
	inventory.NetworkPubnet:  "Public Global Stellar Network ; September 2015",
}

// NetworkPrivate is the test-only network the integration suite runs on.
const NetworkPrivate = "private"

// EnableTestNetwork offers a private network with passphrase in the
// wizard. Only the hidden test flag calls it; it has no preset.
func EnableTestNetwork(passphrase string) {
	if passphrase != "" {
		Passphrases[NetworkPrivate] = passphrase
	}
}

// HostFacts is what Auriga knows about a host before planning.
type HostFacts struct {
	// Existing is the host's adoptable config, nil for a new node, and
	// ExistingText its redacted text (for the review diff).
	Existing     *stellarcfg.Config
	ExistingText []byte
	// SeedPublicKey is the key derived on the host from its current seed,
	// "" when it has none.
	SeedPublicKey string
	// Running is true when stellar-core is active on the host.
	Running bool
	// ServiceWorkDir is where the service runs ("/" for the packaged unit).
	ServiceWorkDir string
	// ArchiveExists is the archive check: "yes", "no" or "unknown".
	ArchiveExists string
}

// Action is a one-off step the deploy runs, which a rollback cannot undo.
type Action struct {
	Kind    string // "new-db" or "new-hist"
	Archive string // for new-hist
	Reason  string
}

// HostPlan is the outcome for one host.
type HostPlan struct {
	Alias    string
	NodeType string
	Config   *stellarcfg.Config
	Rendered []byte
	Diff     string // against the existing config; "" for a new node
	Seed     stellarcfg.Seed
	Actions  []Action
	New      bool
	// Pending lists siblings whose keys are generated at deploy time; the
	// deploy re-plans with the real keys before staging anything.
	Pending []string
	// OwnQuorum is true when the host overrides the org-wide quorum set.
	OwnQuorum bool
	// Preview is the quorum set stellar-core will build for this host.
	Preview []string
}

// Issue is a problem found while planning, tied to the step that fixes it.
type Issue struct {
	Step     int
	Host     string // "" for org-wide issues
	Rule     string
	Message  string
	Severity quorum.Severity
}

func (i Issue) String() string {
	where := StepNames[i.Step]
	if i.Host != "" {
		where += ", " + i.Host
	}
	return fmt.Sprintf("%s: %s", where, i.Message)
}

// Plan is the per-host result handed to the deploy flow.
type Plan struct {
	Org     string
	Network string
	Hosts   []HostPlan
	Issues  []Issue
	// Preview is the generated quorum set for the first host, as text.
	Preview []string
}

// Blocking reports whether any issue stops the wizard.
func (p *Plan) Blocking(consented bool) bool {
	for _, i := range p.Issues {
		if i.Severity == quorum.Block || (i.Severity == quorum.NeedsConsent && !consented) {
			return true
		}
	}
	return false
}

// IssuesFor returns the issues of one step.
func (p *Plan) IssuesFor(step int) []Issue {
	var out []Issue
	for _, i := range p.Issues {
		if i.Step == step {
			out = append(out, i)
		}
	}
	return out
}

// PendingKey stands in for a public key generated at deploy time.
func PendingKey(alias string) string { return "<generated at deploy: " + alias + ">" }

// Input is everything BuildPlan needs.
type Input struct {
	Draft  *Draft
	Facts  map[string]HostFacts
	Preset *presets.Preset // nil when no preset is used
	Render stellarcfg.RenderOptions
	// Generated holds public keys of seeds generated on hosts at deploy
	// time; until then those keys are pending.
	Generated map[string]string
}

// BuildPlan turns the draft into per-host configs, actions and issues.
func BuildPlan(in Input) *Plan {
	d := in.Draft
	p := &Plan{Org: d.OrgName, Network: d.Network}
	add := func(step int, host, rule string, sev quorum.Severity, format string, args ...any) {
		p.Issues = append(p.Issues, Issue{Step: step, Host: host, Rule: rule, Severity: sev, Message: fmt.Sprintf(format, args...)})
	}

	// Step 1: scope.
	if !orgNameRE.MatchString(d.OrgName) {
		add(StepScope, "", "org-name", quorum.Block, "org name %q may only use letters, digits, '.', '_' and '-'", d.OrgName)
	}
	if !stellarcfg.ValidHostname(d.HomeDomain) {
		add(StepScope, "", "home-domain", quorum.Block, "home domain %q is not a valid host name", d.HomeDomain)
	}
	if len(d.Hosts) == 0 {
		add(StepScope, "", "no-hosts", quorum.Block, "select at least one host")
	}
	passphrase, ok := Passphrases[d.Network]
	if !ok {
		add(StepNetwork, "", "network", quorum.Block, "choose testnet or pubnet")
	}

	// Public keys of every host: known, imported, or pending.
	keys := map[string]string{}
	for _, h := range d.Hosts {
		k := d.Keys[h]
		f := in.Facts[h]
		switch k.Mode {
		case KeyKeep:
			if f.SeedPublicKey == "" {
				add(StepKeys, h, "keep-without-seed", quorum.Block, "has no seed to keep; generate one or import one")
			}
			keys[h] = f.SeedPublicKey
		case KeyImport:
			if !strkey.ValidPublicKey(k.ImportedPublicKey) {
				add(StepKeys, h, "import-missing", quorum.Block, "paste the seed to import")
			}
			keys[h] = k.ImportedPublicKey
		case KeyGenerate:
			keys[h] = PendingKey(h)
			if pk := in.Generated[h]; pk != "" {
				keys[h] = pk
			}
		default:
			add(StepKeys, h, "key-mode", quorum.Block, "choose keep, generate or import")
		}
		if k.Mode != KeyKeep && f.SeedPublicKey != "" && f.Running && Validates(d.NodeType) && !k.ReplaceConfirmed {
			add(StepKeys, h, "replace-confirm", quorum.Block,
				"replacing the key of a running validator changes its identity for every org that trusts it; type %s to confirm", h)
		}
		if !aliasRE.MatchString(k.Alias) {
			add(StepKeys, h, "alias", quorum.Block, "node alias %q may only use letters, digits, '.', '_' and '-'", k.Alias)
		}
	}
	seen := map[string]string{}
	for _, h := range d.Hosts {
		if k := keys[h]; k != "" && !strings.HasPrefix(k, "<") {
			if other, dup := seen[k]; dup {
				add(StepKeys, h, "duplicate-key", quorum.Block, "has the same public key as %s", other)
			}
			seen[k] = h
		}
	}

	// Step 6: archives, for types that publish.
	targets := map[string]string{}
	if Publishes(d.NodeType) {
		for _, h := range d.Hosts {
			a, ok := d.Archives[h]
			if !ok {
				add(StepHistory, h, "archive-missing", quorum.Block, "needs a history archive")
				continue
			}
			if err := a.Validate(); err != nil {
				add(StepHistory, h, "archive", quorum.Block, "%v", err)
				continue
			}
			if other, dup := targets[a.PutTarget()]; dup {
				add(StepHistory, h, "shared-archive", quorum.Block, "publishes to the same place as %s; every node needs its own archive", other)
			}
			targets[a.PutTarget()] = h
		}
	}

	// Trusted orgs: own org, preset, manual.
	ownQ := stellarcfg.Quality(d.Quorum.OwnQuality)
	homeDomains := []stellarcfg.HomeDomain{{HomeDomain: d.HomeDomain, Quality: ownQ}}
	var others []stellarcfg.Validator
	if d.Quorum.UsePreset && in.Preset != nil {
		chosen := func(dom string) bool {
			return len(d.Quorum.PresetDomains) == 0 || slices.Contains(d.Quorum.PresetDomains, dom)
		}
		for _, hd := range in.Preset.HomeDomains {
			if chosen(hd.HomeDomain) && hd.HomeDomain != d.HomeDomain {
				homeDomains = append(homeDomains, hd)
			}
		}
		for _, v := range in.Preset.Validators {
			if chosen(v.HomeDomain) && v.HomeDomain != d.HomeDomain {
				others = append(others, v)
			}
		}
	}
	if hds, vals, err := ParseQuorumEntries(d.Quorum.Manual); err != nil {
		add(StepQuorum, "", "manual-parse", quorum.Block, "pasted entries: %v", err)
	} else {
		homeDomains = append(homeDomains, hds...)
		others = append(others, vals...)
	}

	for _, h := range d.Hosts {
		hp, issues := planHost(in, h, passphrase, keys, homeDomains, others)
		p.Issues = append(p.Issues, issues...)
		p.Hosts = append(p.Hosts, hp)
		if p.Preview == nil && !hp.OwnQuorum {
			p.Preview = hp.Preview
		}
	}
	return p
}

// ParseQuorumEntries reads pasted [[HOME_DOMAINS]] and [[VALIDATORS]]
// entries; anything else is refused. Empty text gives nothing.
func ParseQuorumEntries(text string) ([]stellarcfg.HomeDomain, []stellarcfg.Validator, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil, nil
	}
	res, err := stellarcfg.Parse([]byte(text))
	if err != nil {
		return nil, nil, fmt.Errorf("do not parse: %w", err)
	}
	if !onlyQuorumEntries(res.Config) {
		return nil, nil, fmt.Errorf("paste only [[HOME_DOMAINS]] and [[VALIDATORS]] entries")
	}
	return res.Config.HomeDomains, res.Config.Validators, nil
}

// OrgEntries returns the org-wide trusted orgs and validators besides the
// own org: the prefill for a host's override.
func OrgEntries(p *Plan) string {
	for _, hp := range p.Hosts {
		if hp.Config == nil || hp.OwnQuorum {
			continue
		}
		var hds []stellarcfg.HomeDomain
		for _, h := range hp.Config.HomeDomains {
			if h.HomeDomain != hp.Config.NodeHomeDomain {
				hds = append(hds, h)
			}
		}
		var vals []stellarcfg.Validator
		for _, v := range hp.Config.Validators {
			if v.HomeDomain != hp.Config.NodeHomeDomain {
				vals = append(vals, v)
			}
		}
		return stellarcfg.RenderQuorumEntries(hds, vals)
	}
	return ""
}

// onlyQuorumEntries reports whether c sets nothing but [[HOME_DOMAINS]]
// and [[VALIDATORS]].
func onlyQuorumEntries(c *stellarcfg.Config) bool {
	rest := *c
	rest.HomeDomains, rest.Validators = nil, nil
	return reflect.DeepEqual(rest, stellarcfg.Config{})
}

var aliasRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

func planHost(in Input, h, passphrase string, keys map[string]string, homeDomains []stellarcfg.HomeDomain, others []stellarcfg.Validator) (HostPlan, []Issue) {
	d := in.Draft
	f := in.Facts[h]
	k := d.Keys[h]
	adv := d.Advanced[h]
	var issues []Issue
	add := func(step int, rule string, sev quorum.Severity, format string, args ...any) {
		issues = append(issues, Issue{Step: step, Host: h, Rule: rule, Severity: sev, Message: fmt.Sprintf(format, args...)})
	}

	hp := HostPlan{Alias: h, NodeType: d.NodeType, New: f.Existing == nil}
	var c stellarcfg.Config
	if f.Existing != nil {
		c = *f.Existing // keep unknown keys and existing values
	}

	// Step 2: network.
	if f.Existing != nil && f.Existing.NetworkPassphrase != "" && f.Existing.NetworkPassphrase != passphrase {
		if slices.Contains(d.NewDBAccepted, h) {
			hp.Actions = append(hp.Actions, Action{Kind: "new-db", Reason: "the network changes; this wipes the node's local ledger state"})
		} else {
			add(StepNetwork, "network-change", quorum.Block, "is on another network; switching runs new-db, which wipes its local ledger state; accept it to continue")
		}
	}
	c.NetworkPassphrase = passphrase

	// Steps 3 and 4: identity.
	c.NodeIsValidator = stellarcfg.Ptr(Validates(d.NodeType))
	c.NodeHomeDomain = d.HomeDomain
	mode := stellarcfg.SeedMode(k.Mode)
	hp.Seed = stellarcfg.Seed{Mode: mode, Alias: k.Alias}
	if !strings.HasPrefix(keys[h], "<") {
		hp.Seed.PublicKey = keys[h]
	}
	c.NodeSeed = &hp.Seed

	// Step 7: ports and storage. Existing values are kept; new nodes get the
	// operator's choices or absolute defaults.
	c.PublicHTTPPort = stellarcfg.Ptr(false)
	if adv.HTTPPort != 0 {
		c.HTTPPort = stellarcfg.Ptr(adv.HTTPPort)
	} else if c.HTTPPort == nil {
		c.HTTPPort = stellarcfg.Ptr(DefaultHTTPPort)
	}
	if adv.PeerPort != 0 {
		c.PeerPort = stellarcfg.Ptr(adv.PeerPort)
	} else if c.PeerPort == nil {
		c.PeerPort = stellarcfg.Ptr(DefaultPeerPort)
	}
	if adv.Database != "" {
		c.Database = adv.Database
	}
	if c.Database == "" {
		add(StepAdvanced, "database-required", quorum.Block, "needs a database connection string, e.g. sqlite3:///var/lib/stellar/stellar.db or postgresql://dbname=stellar user=stellar")
	}
	switch {
	case adv.BucketDir != "":
		c.BucketDirPath = adv.BucketDir
	case c.BucketDirPath == "":
		c.BucketDirPath = DefaultBucketDir
	}
	switch {
	case adv.LogFile != "":
		c.LogFilePath = stellarcfg.Ptr(adv.LogFile)
	case c.LogFilePath == nil:
		c.LogFilePath = stellarcfg.Ptr(DefaultLogFile)
	}
	if f.Existing == nil {
		hp.Actions = append(hp.Actions, Action{Kind: "new-db", Reason: "a new node needs its database initialised"})
	}

	// Step 5: quorum, or this host's override from step 7. Siblings only:
	// the node never lists itself.
	quorumStep := StepQuorum
	if adv.QuorumOverride {
		hp.OwnQuorum, quorumStep = true, StepAdvanced
		hds, vals, err := ParseQuorumEntries(adv.QuorumEntries)
		if err != nil {
			add(StepAdvanced, "override-parse", quorum.Block, "quorum override: %v", err)
		}
		homeDomains = append([]stellarcfg.HomeDomain{homeDomains[0]}, withoutDomain(hds, d.HomeDomain)...)
		others = withoutValidatorDomain(vals, d.HomeDomain)
	}
	c.HomeDomains = append([]stellarcfg.HomeDomain(nil), homeDomains...)
	c.Validators = nil
	var peers []string
	for _, s := range d.Hosts {
		if s == h {
			continue
		}
		sk := d.Keys[s]
		v := stellarcfg.Validator{Name: sk.Alias, HomeDomain: d.HomeDomain, PublicKey: keys[s], Address: sk.Address}
		if strings.HasPrefix(keys[s], "<") {
			hp.Pending = append(hp.Pending, s)
		}
		if a, ok := d.Archives[s]; ok && Publishes(d.NodeType) {
			v.History = a.GetTemplate()
		}
		c.Validators = append(c.Validators, v)
		if sk.Address != "" {
			peers = append(peers, sk.Address)
		}
	}
	c.Validators = append(c.Validators, others...)
	c.KnownPeers = dedupe(append(peers, adv.ExtraPeers...))
	if len(c.KnownPeers) == 0 {
		c.KnownPeers = nil
	}
	if adv.PreferQuorum {
		var pk []string
		for _, v := range c.Validators {
			if !strings.HasPrefix(v.PublicKey, "<") {
				pk = append(pk, v.PublicKey)
			}
		}
		c.PreferredPeerKeys = pk
	}

	// Step 6: own archive.
	if a, ok := d.Archives[h]; ok && Publishes(d.NodeType) {
		hist, err := a.History(true)
		if err == nil {
			c.History = slices.DeleteFunc(append([]stellarcfg.HistoryArchive(nil), c.History...),
				func(x stellarcfg.HistoryArchive) bool { return x.Name == hist.Name })
			c.History = append(c.History, hist)
			if f.ArchiveExists != "yes" {
				hp.Actions = append(hp.Actions, Action{Kind: "new-hist", Archive: a.Name,
					Reason: "initialises the archive; running it on an existing archive erases it"})
			}
		}
	}

	// Quorum checks, and consent for zero fault tolerance.
	self := quorum.Self{Name: k.Alias, PublicKey: keys[h], HomeDomain: d.HomeDomain}
	if strings.HasPrefix(self.PublicKey, "<") {
		self.PublicKey = ""
	}
	var running *stellarcfg.Config
	if f.Existing != nil {
		running = f.Existing
	}
	c.UnsafeQuorum = nil
	for _, qi := range quorum.Check(&c, self, quorum.CheckOptions{Running: running}) {
		if qi.Rule == "zero-tolerance" && d.Quorum.UnsafeConsent {
			c.UnsafeQuorum = stellarcfg.Ptr(true)
			qi.Severity = quorum.Warn
		}
		msg := qi.Message
		if qi.Entry != "" {
			msg = qi.Entry + " " + msg
		}
		issues = append(issues, Issue{Step: quorumStep, Host: h, Rule: qi.Rule, Severity: qi.Severity, Message: msg})
	}

	// Per-config validation, skipping keys that are pending.
	for _, vi := range stellarcfg.Validate(&c, stellarcfg.ValidateOptions{ServiceWorkDir: f.ServiceWorkDir}) {
		if vi.Rule == "public-key" && strings.Contains(vi.Message, "<generated at deploy") {
			continue
		}
		if vi.Rule == "database-missing" {
			continue // the planner's own database-required rule covers it
		}
		sev := quorum.Warn
		if vi.Severity == stellarcfg.Block {
			sev = quorum.Block
		}
		step := stepForKey(vi.Key)
		if step == StepQuorum {
			step = quorumStep
		}
		issues = append(issues, Issue{Step: step, Host: h, Rule: vi.Rule, Severity: sev, Message: vi.Key + ": " + vi.Message})
	}

	hp.Config = &c
	if set := quorum.Generate(&c, self); set != nil {
		hp.Preview = set.Lines()
	}
	rendered, err := stellarcfg.Render(&c, in.Render)
	if err != nil {
		add(StepAdvanced, "render", quorum.Block, "%v", err)
		return hp, issues
	}
	hp.Rendered = rendered
	if f.Existing != nil && f.ExistingText != nil {
		hp.Diff = stellarcfg.Diff("on the host now", "after the deploy", f.ExistingText, rendered)
	}
	return hp, issues
}

// withoutDomain drops entries for the own org, which the override cannot
// change (its validators are the siblings, set in step 4).
func withoutDomain(hds []stellarcfg.HomeDomain, own string) []stellarcfg.HomeDomain {
	var out []stellarcfg.HomeDomain
	for _, h := range hds {
		if h.HomeDomain != own {
			out = append(out, h)
		}
	}
	return out
}

func withoutValidatorDomain(vals []stellarcfg.Validator, own string) []stellarcfg.Validator {
	var out []stellarcfg.Validator
	for _, v := range vals {
		if v.HomeDomain != own {
			out = append(out, v)
		}
	}
	return out
}

// stepForKey maps a config key to the step where it is set.
func stepForKey(key string) int {
	switch {
	case strings.HasPrefix(key, "[[VALIDATORS]]"), strings.HasPrefix(key, "[[HOME_DOMAINS]]"):
		return StepQuorum
	case strings.HasPrefix(key, "[HISTORY"):
		return StepHistory
	case key == "NODE_SEED":
		return StepKeys
	case key == "NETWORK_PASSPHRASE":
		return StepNetwork
	case key == "NODE_HOME_DOMAIN":
		return StepScope
	}
	return StepAdvanced
}

func dedupe(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
