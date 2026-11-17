package wizard

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/inventory"
	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/quorum"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

var update = flag.Bool("update", false, "update golden fixture-org renders")

// Public test-vector keys (see internal/strkey and test/integration).
const (
	keyA = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	keyB = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
	keyC = "GDVEU3DD4KOFECV66VIHWEZOYX4ZKR3WV27L464SIIPOU2IUI3JCZA57"
)

var renderOpt = stellarcfg.RenderOptions{Version: "v0.0.0-test", Time: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}

func preset(t *testing.T, n presets.Network) *presets.Preset {
	t.Helper()
	p, err := presets.Load("", n)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testnetBasic is the fixture org "testnet Basic": three new Basic
// validators, keys imported, own org MEDIUM, SDF testnet preset.
func testnetBasic() (*Draft, map[string]HostFacts) {
	d := NewDraft()
	d.OrgName, d.HomeDomain = "acme", "acme.example"
	d.Hosts = []string{"acme-a", "acme-b", "acme-c"}
	d.Network, d.NodeType = "testnet", TypeBasic
	for h, k := range map[string]string{"acme-a": keyA, "acme-b": keyB, "acme-c": keyC} {
		d.Keys[h] = KeyInput{Mode: KeyImport, Alias: h, Address: "core-" + h[len(h)-1:] + ".acme.example:11625", ImportedPublicKey: k}
		d.Advanced[h] = AdvancedInput{Database: "sqlite3:///var/lib/stellar/stellar.db"}
	}
	d.Quorum.UnsafeConsent = true
	facts := map[string]HostFacts{}
	for _, h := range d.Hosts {
		facts[h] = HostFacts{ServiceWorkDir: "/", ArchiveExists: "unknown"}
	}
	return d, facts
}

// pubnetFull is the fixture org "pubnet Full": three Full Validators with
// their own S3 archives, own org HIGH, Tier 1 preset.
func pubnetFull() (*Draft, map[string]HostFacts) {
	d, facts := testnetBasic()
	d.Network, d.NodeType = "pubnet", TypeFull
	d.Quorum.OwnQuality, d.Quorum.UnsafeConsent = string(stellarcfg.High), false
	for _, h := range d.Hosts {
		d.Archives[h] = stellarcfg.ArchiveSpec{Name: h, Backend: stellarcfg.BackendS3,
			URL: "https://history-" + h[len(h)-1:] + ".acme.example", Bucket: "acme-history", Prefix: h}
		d.Advanced[h] = AdvancedInput{Database: "postgresql://dbname=stellar user=stellar", PreferQuorum: true}
	}
	return d, facts
}

func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs:\n%s", path, stellarcfg.Diff("golden", "got", want, got))
	}
}

func blocking(p *Plan) []Issue {
	var out []Issue
	for _, i := range p.Issues {
		if i.Severity == quorum.Block || i.Severity == quorum.NeedsConsent {
			out = append(out, i)
		}
	}
	return out
}

func TestFixtureOrgs(t *testing.T) {
	for name, fx := range map[string]struct {
		draft  func() (*Draft, map[string]HostFacts)
		preset presets.Network
	}{
		"testnet-basic": {testnetBasic, presets.Testnet},
		"pubnet-full":   {pubnetFull, presets.Pubnet},
	} {
		t.Run(name, func(t *testing.T) {
			d, facts := fx.draft()
			p := BuildPlan(Input{Draft: d, Facts: facts, Preset: preset(t, fx.preset), Render: renderOpt})
			if b := blocking(p); len(b) != 0 {
				t.Fatalf("blocking issues: %v", b)
			}
			for _, hp := range p.Hosts {
				golden(t, filepath.Join("testdata", name, hp.Alias+".cfg"), hp.Rendered)
				if len(hp.Pending) != 0 || !hp.New || hp.Actions[0].Kind != "new-db" {
					t.Errorf("%s: pending %v new %v actions %+v", hp.Alias, hp.Pending, hp.New, hp.Actions)
				}
				for _, v := range hp.Config.Validators {
					if v.PublicKey == hp.Seed.PublicKey {
						t.Errorf("%s lists itself", hp.Alias)
					}
				}
			}
			if len(p.Preview) == 0 {
				t.Error("no quorum preview")
			}
		})
	}
}

func TestTestnetBasicDetails(t *testing.T) {
	d, facts := testnetBasic()
	p := BuildPlan(Input{Draft: d, Facts: facts, Preset: preset(t, presets.Testnet), Render: renderOpt})
	c := p.Hosts[0].Config
	if c.UnsafeQuorum == nil || !*c.UnsafeQuorum {
		t.Error("consented zero-tolerance quorum did not write UNSAFE_QUORUM")
	}
	if len(c.Validators) != 5 || !slices.Equal(c.KnownPeers, []string{"core-b.acme.example:11625", "core-c.acme.example:11625"}) {
		t.Errorf("validators %d peers %v", len(c.Validators), c.KnownPeers)
	}
	if *c.NodeIsValidator != true || c.History != nil || *c.LogFilePath != DefaultLogFile || c.BucketDirPath != DefaultBucketDir {
		t.Errorf("%+v", c)
	}
	// Without consent, the zero-tolerance quorum blocks.
	d.Quorum.UnsafeConsent = false
	p = BuildPlan(Input{Draft: d, Facts: facts, Preset: preset(t, presets.Testnet), Render: renderOpt})
	if !p.Blocking(false) || !hasRule(p, "zero-tolerance", quorum.NeedsConsent) || p.Hosts[0].Config.UnsafeQuorum != nil {
		t.Errorf("no consent: %v", p.Issues)
	}
}

func TestPubnetFullDetails(t *testing.T) {
	d, facts := pubnetFull()
	p := BuildPlan(Input{Draft: d, Facts: facts, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	hp := p.Hosts[1]
	c := hp.Config
	if c.UnsafeQuorum != nil {
		t.Error("UNSAFE_QUORUM written for an 11-entity quorum")
	}
	if len(c.History) != 1 || c.History[0].Put != "aws s3 cp {0} s3://acme-history/acme-b/{1}" {
		t.Errorf("history %+v", c.History)
	}
	if len(hp.Actions) != 2 || hp.Actions[1].Kind != "new-hist" || hp.Actions[1].Archive != "acme-b" {
		t.Errorf("actions %+v", hp.Actions)
	}
	if len(c.PreferredPeerKeys) != 32 {
		t.Errorf("preferred peer keys %d, want 2 siblings + 30 Tier 1", len(c.PreferredPeerKeys))
	}
	for _, v := range c.Validators {
		if v.HomeDomain == "acme.example" && !strings.HasPrefix(v.History, "curl -sf https://history-") {
			t.Errorf("sibling %s has no public get: %q", v.Name, v.History)
		}
	}
	// An existing archive skips new-hist.
	facts["acme-b"] = HostFacts{ServiceWorkDir: "/", ArchiveExists: "yes"}
	p = BuildPlan(Input{Draft: d, Facts: facts, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	if len(p.Hosts[1].Actions) != 1 {
		t.Errorf("existing archive still initialised: %+v", p.Hosts[1].Actions)
	}
}

func hasRule(p *Plan, rule string, sev quorum.Severity) bool {
	for _, i := range p.Issues {
		if i.Rule == rule && i.Severity == sev {
			return true
		}
	}
	return false
}

func TestPlanBlockingRules(t *testing.T) {
	adopted := func() *stellarcfg.Config {
		res, err := stellarcfg.Parse([]byte(`NETWORK_PASSPHRASE="Public Global Stellar Network ; September 2015"
NODE_SEED="<redacted:` + keyA + `> acme-a"
NODE_IS_VALIDATOR=true
DATABASE="postgresql://dbname=stellar"
COMMANDS=["ll?level=info"]
[[VALIDATORS]]
NAME="old-friend"
HOME_DOMAIN="friend.example"
PUBLIC_KEY="` + keyC + `"
`))
		if err != nil {
			t.Fatal(err)
		}
		return res.Config
	}
	tests := []struct {
		name string
		mod  func(d *Draft, f map[string]HostFacts)
		rule string
		step int
	}{
		{"org name", func(d *Draft, _ map[string]HostFacts) { d.OrgName = "bad name" }, "org-name", StepScope},
		{"home domain", func(d *Draft, _ map[string]HostFacts) { d.HomeDomain = "nodots" }, "home-domain", StepScope},
		{"no hosts", func(d *Draft, _ map[string]HostFacts) { d.Hosts = nil }, "no-hosts", StepScope},
		{"network", func(d *Draft, _ map[string]HostFacts) { d.Network = "futurenet" }, "network", StepNetwork},
		{"network change", func(d *Draft, f map[string]HostFacts) {
			f["acme-a"] = HostFacts{Existing: adopted(), SeedPublicKey: keyA}
		}, "network-change", StepNetwork},
		{"keep without seed", func(d *Draft, _ map[string]HostFacts) { d.Keys["acme-a"] = KeyInput{Mode: KeyKeep, Alias: "acme-a"} }, "keep-without-seed", StepKeys},
		{"import missing", func(d *Draft, _ map[string]HostFacts) { d.Keys["acme-a"] = KeyInput{Mode: KeyImport, Alias: "acme-a"} }, "import-missing", StepKeys},
		{"replace running key", func(d *Draft, f map[string]HostFacts) {
			f["acme-a"] = HostFacts{SeedPublicKey: "GAAAA", Running: true, ServiceWorkDir: "/"}
		}, "replace-confirm", StepKeys},
		{"duplicate key", func(d *Draft, _ map[string]HostFacts) {
			k := d.Keys["acme-b"]
			k.ImportedPublicKey = keyA
			d.Keys["acme-b"] = k
		}, "duplicate-key", StepKeys},
		{"alias", func(d *Draft, _ map[string]HostFacts) { k := d.Keys["acme-a"]; k.Alias = "a b"; d.Keys["acme-a"] = k }, "alias", StepKeys},
		{"database", func(d *Draft, _ map[string]HostFacts) { d.Advanced["acme-a"] = AdvancedInput{} }, "database-required", StepAdvanced},
		{"high own org of two", func(d *Draft, _ map[string]HostFacts) {
			d.Hosts = d.Hosts[:2]
			d.Quorum.OwnQuality = "HIGH"
		}, "redundancy", StepQuorum},
		{"manual paste", func(d *Draft, _ map[string]HostFacts) { d.Quorum.Manual = "[[VALIDATORS]\n" }, "manual-parse", StepQuorum},
		{"manual extra keys", func(d *Draft, _ map[string]HostFacts) { d.Quorum.Manual = "DATABASE=\"x\"\n" }, "manual-parse", StepQuorum},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, f := testnetBasic()
			tt.mod(d, f)
			p := BuildPlan(Input{Draft: d, Facts: f, Preset: preset(t, presets.Testnet), Render: renderOpt})
			found := false
			for _, i := range p.IssuesFor(tt.step) {
				if i.Rule == tt.rule && i.Severity == quorum.Block {
					found = true
				}
			}
			if !found {
				t.Errorf("rule %s not raised in step %d; issues: %v", tt.rule, tt.step, p.Issues)
			}
		})
	}
	// Archive rules for a publishing org.
	d, f := pubnetFull()
	a := d.Archives["acme-c"]
	a.Prefix = "acme-b"
	d.Archives["acme-c"] = a
	delete(d.Archives, "acme-a")
	p := BuildPlan(Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	if !hasRule(p, "shared-archive", quorum.Block) || !hasRule(p, "archive-missing", quorum.Block) {
		t.Errorf("archive rules: %v", p.Issues)
	}
}

func TestAdoptedHostKeepsUnknownKeysAndWarnsAboutRemovals(t *testing.T) {
	d, f := pubnetFull()
	res, _ := stellarcfg.Parse([]byte(`NETWORK_PASSPHRASE="Public Global Stellar Network ; September 2015"
NODE_SEED="<redacted:` + keyA + `> acme-a"
NODE_IS_VALIDATOR=true
DATABASE="postgresql://dbname=old"
COMMANDS=["ll?level=info"]
[[HOME_DOMAINS]]
HOME_DOMAIN="friend.example"
QUALITY="HIGH"
[[VALIDATORS]]
NAME="old-friend"
HOME_DOMAIN="friend.example"
PUBLIC_KEY="GA7ZBBA3T5E6SX2AVZXIL2Q7SJDHIXH6NBSFPNEF6L3YZT5DCWFFVA7H"
`))
	f["acme-a"] = HostFacts{Existing: res.Config, SeedPublicKey: keyA, Running: true, ServiceWorkDir: "/", ArchiveExists: "yes"}
	d.Keys["acme-a"] = KeyInput{Mode: KeyKeep, Alias: "acme-a", Address: "core-a.acme.example:11625"}
	d.Advanced["acme-a"] = AdvancedInput{PreferQuorum: true}
	p := BuildPlan(Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	hp := p.Hosts[0]
	if hp.New || hp.Config.Database != "postgresql://dbname=old" || len(hp.Config.Unknown) != 1 {
		t.Errorf("adopted host: new %v db %q unknown %v", hp.New, hp.Config.Database, hp.Config.Unknown)
	}
	if !strings.Contains(string(hp.Rendered), `COMMANDS=["ll?level=info"]`) || hp.Seed.Mode != stellarcfg.SeedKeep {
		t.Error("unknown key or kept seed lost")
	}
	if !hasRule(p, "removed", quorum.Warn) || len(hp.Actions) != 0 {
		t.Errorf("removed-validator warning or actions: %v %+v", p.Issues, hp.Actions)
	}
}

func TestGeneratedKeysArePendingUntilKnown(t *testing.T) {
	d, f := testnetBasic()
	for _, h := range d.Hosts {
		k := d.Keys[h]
		k.Mode, k.ImportedPublicKey = KeyGenerate, ""
		d.Keys[h] = k
	}
	in := Input{Draft: d, Facts: f, Preset: preset(t, presets.Testnet), Render: renderOpt}
	p := BuildPlan(in)
	if b := blocking(p); len(b) != 0 {
		t.Fatalf("pending keys should not block: %v", b)
	}
	if !slices.Equal(p.Hosts[0].Pending, []string{"acme-b", "acme-c"}) || p.Hosts[0].Seed.PublicKey != "" {
		t.Errorf("pending %v seed %+v", p.Hosts[0].Pending, p.Hosts[0].Seed)
	}
	if !strings.Contains(string(p.Hosts[0].Rendered), `PUBLIC_KEY = "<generated at deploy: acme-b>"`) {
		t.Error("pending key not shown in the render")
	}
	in.Generated = map[string]string{"acme-a": keyA, "acme-b": keyB, "acme-c": keyC}
	p = BuildPlan(in)
	if len(p.Hosts[0].Pending) != 0 || p.Hosts[0].Seed.Mode != stellarcfg.SeedGenerate || p.Hosts[0].Seed.PublicKey != keyA ||
		strings.Contains(string(p.Hosts[0].Rendered), "<generated") {
		t.Errorf("after generation: %+v\n%s", p.Hosts[0].Seed, p.Hosts[0].Rendered)
	}
}

func TestDraftRoundTripHasNoSecrets(t *testing.T) {
	dir := t.TempDir()
	d, _ := pubnetFull()
	d.Step = StepQuorum
	if err := SaveDraft(dir, d); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(DraftPath(dir, "acme"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("draft mode %o", fi.Mode().Perm())
	}
	got, ok, err := LoadDraft(dir, "acme")
	if err != nil || !ok || got.Step != StepQuorum || got.Archives["acme-b"].Prefix != "acme-b" || got.Keys["acme-c"].ImportedPublicKey != keyC {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if _, ok, _ := LoadDraft(dir, "other"); ok {
		t.Error("missing draft reported present")
	}
	if err := SaveDraft(dir, &Draft{OrgName: "../escape"}); err == nil {
		t.Error("path in org name accepted")
	}
}

func TestPerHostQuorumOverride(t *testing.T) {
	d, f := pubnetFull()
	in := Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt}
	base := BuildPlan(in)
	entries := OrgEntries(base)
	if !strings.Contains(entries, "[[HOME_DOMAINS]]") || strings.Contains(entries, "acme.example") || strings.Count(entries, "[[VALIDATORS]]") != 30 {
		t.Fatalf("prefill should hold the 30 preset validators and no own-org entries:\n%.300s", entries)
	}

	// acme-c trusts only the first three Tier 1 orgs; its own org stays.
	hds, vals, err := ParseQuorumEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	var keptVals []stellarcfg.Validator
	for _, h := range hds[:3] {
		keep[h.HomeDomain] = true
	}
	for _, v := range vals {
		if keep[v.HomeDomain] {
			keptVals = append(keptVals, v)
		}
	}
	a := d.Advanced["acme-c"]
	a.QuorumOverride = true
	a.QuorumEntries = stellarcfg.RenderQuorumEntries(hds[:3], keptVals) +
		"\n[[HOME_DOMAINS]]\nHOME_DOMAIN=\"acme.example\"\nQUALITY=\"LOW\"\n" // own org: ignored
	d.Advanced["acme-c"] = a
	p := BuildPlan(in)
	if b := blocking(p); len(b) != 0 {
		t.Fatalf("blocking: %v", b)
	}
	c, other := p.Hosts[2], p.Hosts[0]
	if !c.OwnQuorum || other.OwnQuorum {
		t.Fatal("override flags wrong")
	}
	if len(c.Config.Validators) != 2+9 || len(other.Config.Validators) != 2+30 {
		t.Errorf("validators: override %d, others %d", len(c.Config.Validators), len(other.Config.Validators))
	}
	if c.Config.HomeDomains[0] != (stellarcfg.HomeDomain{HomeDomain: "acme.example", Quality: stellarcfg.High}) || len(c.Config.HomeDomains) != 4 {
		t.Errorf("own org not kept as configured: %+v", c.Config.HomeDomains)
	}
	if !strings.Contains(strings.Join(c.Preview, "\n"), "3 of 4") || strings.Join(c.Preview, "") == strings.Join(other.Preview, "") {
		t.Errorf("override preview:\n%s", strings.Join(c.Preview, "\n"))
	}
	if strings.Join(p.Preview, "") != strings.Join(other.Preview, "") {
		t.Error("org preview taken from the overriding host")
	}

	// Override problems belong to the Advanced step.
	a.QuorumEntries = "[[VALIDATORS]\n"
	d.Advanced["acme-c"] = a
	p = BuildPlan(in)
	found := false
	for _, i := range p.IssuesFor(StepAdvanced) {
		found = found || (i.Rule == "override-parse" && i.Host == "acme-c")
	}
	if !found {
		t.Errorf("parse error not at Advanced: %v", p.Issues)
	}
	a.QuorumEntries = "[[HOME_DOMAINS]]\nHOME_DOMAIN=\"x.example\"\nQUALITY=\"HIGH\"\n[[VALIDATORS]]\nNAME=\"x1\"\nHOME_DOMAIN=\"x.example\"\nPUBLIC_KEY=\"" + keyC + "\"\nHISTORY=\"curl -sf https://h/{0} -o {1}\"\n"
	d.Advanced["acme-c"] = a
	p = BuildPlan(in)
	found = false
	for _, i := range p.IssuesFor(StepAdvanced) {
		found = found || (i.Rule == "redundancy" && i.Host == "acme-c")
	}
	if !found {
		t.Errorf("quorum rule for the override not at Advanced: %v", p.Issues)
	}
	for _, i := range p.IssuesFor(StepQuorum) {
		if i.Host == "acme-c" {
			t.Errorf("override issue shown at Quorum: %v", i)
		}
	}
}

func TestPrivateTestNetwork(t *testing.T) {
	const pass = "Auriga Integration Network ; November 2026"
	d, f := testnetBasic()
	d.Network = NetworkPrivate
	if p := BuildPlan(Input{Draft: d, Facts: f, Render: renderOpt}); len(blocking(p)) == 0 {
		t.Fatal("private network accepted without the test flag")
	}
	EnableTestNetwork(pass)
	t.Cleanup(func() { delete(Passphrases, NetworkPrivate); inventory.SetTestNetwork("") })
	if inventory.NetworkFromPassphrase(pass) != NetworkPrivate {
		t.Error("the fleet does not know the test network")
	}
	p := BuildPlan(Input{Draft: d, Facts: f, Render: renderOpt})
	if b := blocking(p); len(b) != 0 {
		t.Fatalf("blocking: %v", b)
	}
	for _, hp := range p.Hosts {
		if hp.Config.NetworkPassphrase != pass {
			t.Errorf("%s: %q", hp.Alias, hp.Config.NetworkPassphrase)
		}
	}
}

// An org of Basic validators with nothing outside it has no archive in any
// quorum: stellar-core refuses it without UNSAFE_QUORUM (found deploying to
// the integration stack; fixture L in internal/quorum/testdata/core30).
func TestNoArchivesNeedsConsent(t *testing.T) {
	d, f := testnetBasic()
	d.Quorum.UsePreset, d.Quorum.UnsafeConsent = false, false
	d.Quorum.OwnQuality = "MEDIUM"
	p := BuildPlan(Input{Draft: d, Facts: f, Render: renderOpt})
	found := false
	for _, i := range p.Issues {
		found = found || (i.Rule == "archives-blocking" && i.Severity == quorum.NeedsConsent)
	}
	if !found || !p.Blocking(false) {
		t.Fatalf("no consent asked: %v", p.Issues)
	}
	d.Quorum.UnsafeConsent = true
	p = BuildPlan(Input{Draft: d, Facts: f, Render: renderOpt})
	if p.Blocking(true) {
		t.Fatalf("still blocked after consent: %v", p.Issues)
	}
	for _, hp := range p.Hosts {
		if hp.Config.UnsafeQuorum == nil || !*hp.Config.UnsafeQuorum {
			t.Errorf("%s: UNSAFE_QUORUM not written after consent", hp.Alias)
		}
	}
}

// An archive the node already publishes is kept as it is and never
// initialised again (new-hist would erase it).
func TestAdoptedArchiveIsNotReinitialised(t *testing.T) {
	d, f := pubnetFull()
	existing := stellarcfg.HistoryArchive{Name: "local", Get: "cp /var/lib/stellar/history/{0} {1}",
		Put: "cp {0} /var/lib/stellar/history/{1}", Mkdir: "mkdir -p /var/lib/stellar/history/{0}"}
	h := d.Hosts[0]
	fa := f[h]
	fa.Existing = &stellarcfg.Config{NetworkPassphrase: Passphrases["pubnet"], NodeHomeDomain: d.HomeDomain, History: []stellarcfg.HistoryArchive{existing}}
	fa.ArchiveExists = "unknown" // a custom archive without a URL cannot be checked
	f[h] = fa
	d.Archives[h] = stellarcfg.ArchiveFromHistory(existing)
	p := BuildPlan(Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	for _, i := range p.IssuesFor(StepHistory) {
		if i.Host == h && i.Severity == quorum.Block {
			t.Errorf("blocking history issue for the adopted archive: %v", i)
		}
	}
	for _, a := range p.Hosts[0].Actions {
		if a.Kind == "new-hist" {
			t.Errorf("new-hist planned for the archive %s already publishes", h)
		}
	}
	if !slices.Contains(p.Hosts[0].Config.History, existing) {
		t.Errorf("history %+v", p.Hosts[0].Config.History)
	}
	// Moving it elsewhere is a new archive.
	moved := d.Archives[h]
	moved.Put = "cp {0} /srv/history/{1}"
	d.Archives[h] = moved
	p = BuildPlan(Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt})
	if !slices.ContainsFunc(p.Hosts[0].Actions, func(a Action) bool { return a.Kind == "new-hist" }) {
		t.Error("no new-hist for a moved archive")
	}
}

// The same custom commands on several hosts are each host's own archive
// (cp to a local directory); with the same public URL they are not.
func TestCustomArchivesOnSeveralHosts(t *testing.T) {
	d, f := pubnetFull()
	local := stellarcfg.ArchiveSpec{Backend: stellarcfg.BackendCustom, Get: "cp /var/lib/stellar/history/{0} {1}",
		Put: "cp {0} /var/lib/stellar/history/{1}", Mkdir: "mkdir -p /var/lib/stellar/history/{0}"}
	for _, h := range d.Hosts {
		a := local
		a.Name = h
		d.Archives[h] = a
	}
	in := Input{Draft: d, Facts: f, Preset: preset(t, presets.Pubnet), Render: renderOpt}
	if p := BuildPlan(in); hasRule(p, "shared-archive", quorum.Block) || !hasRule(p, "archive-local-get", quorum.Warn) {
		t.Errorf("local custom archives: %v", p.IssuesFor(StepHistory))
	}
	for _, h := range d.Hosts {
		a := d.Archives[h]
		a.URL = "https://history.acme.example"
		d.Archives[h] = a
	}
	if !hasRule(BuildPlan(in), "shared-archive", quorum.Block) {
		t.Error("custom archives with one URL not reported as shared")
	}
}
