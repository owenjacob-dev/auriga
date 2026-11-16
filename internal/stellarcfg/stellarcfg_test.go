package stellarcfg

import (
	"bytes"
	"errors"
	"flag"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

var update = flag.Bool("update", false, "update golden renders")

var renderOpt = RenderOptions{Version: "v0.0.0-test", Time: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}

func input(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "input", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "render", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("render differs from %s:\n%s", p, Diff("golden", "got", want, got))
	}
}

var adoptable = []string{"sdf-pubnet-validator-full.cfg", "package-testnet-validator.redacted.cfg", "adopted-unknown-keys.cfg", "itest-validator.redacted.cfg"}

func mustParse(t *testing.T, text []byte) *Result {
	t.Helper()
	res, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRenderGolden(t *testing.T) {
	for _, name := range adoptable {
		t.Run(name, func(t *testing.T) {
			out, err := Render(mustParse(t, input(t, name)).Config, renderOpt)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name, out)
			// The render is valid TOML.
			var m map[string]any
			if err := toml.Unmarshal(out, &m); err != nil {
				t.Fatalf("render is not valid TOML: %v", err)
			}
		})
	}
}

// testnetBasic is a fixture org's node built from the model, as the wizard
// will (SPEC.md golden list: testnet Basic).
func testnetBasic() *Config {
	return &Config{
		NetworkPassphrase: "Test SDF Network ; September 2015",
		NodeSeed:          &Seed{Mode: SeedGenerate, Alias: "acme-a"},
		NodeIsValidator:   Ptr(true),
		NodeHomeDomain:    "acme.example",
		HTTPPort:          Ptr(11626),
		PublicHTTPPort:    Ptr(false),
		PeerPort:          Ptr(11625),
		Database:          "sqlite3:///var/lib/stellar/stellar.db",
		BucketDirPath:     "/var/lib/stellar/buckets",
		LogFilePath:       Ptr("/var/log/stellar/stellar-core.log"),
		HomeDomains: []HomeDomain{
			{HomeDomain: "testnet.stellar.org", Quality: High},
			{HomeDomain: "acme.example", Quality: Medium},
		},
		Validators: []Validator{
			{Name: "sdftest3", HomeDomain: "testnet.stellar.org", PublicKey: "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q", History: "curl -sf http://history.stellar.org/prd/core-testnet/core_testnet_003/{0} -o {1}"},
			{Name: "acme-b", HomeDomain: "acme.example", PublicKey: "GDVEU3DD4KOFECV66VIHWEZOYX4ZKR3WV27L464SIIPOU2IUI3JCZA57", Address: "core-b.acme.example:11625"},
		},
	}
}

func TestRenderTestnetBasicGolden(t *testing.T) {
	out, err := Render(testnetBasic(), renderOpt)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "testnet-basic.cfg", out)
	if issues := Validate(testnetBasic(), ValidateOptions{ServiceWorkDir: "/"}); len(issues) != 0 {
		t.Errorf("fixture has issues: %v", issues)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, name := range adoptable {
		t.Run(name, func(t *testing.T) {
			first := mustParse(t, input(t, name)).Config
			r1, err := Render(first, renderOpt)
			if err != nil {
				t.Fatal(err)
			}
			second := mustParse(t, r1).Config
			r2, err := Render(second, renderOpt)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(r1, r2) {
				t.Errorf("render is not stable:\n%s", Diff("first", "second", r1, r2))
			}
			// The model survives, apart from source order of sorted lists.
			if !sameModel(first, second) {
				t.Errorf("model changed after a round trip:\nfirst  %+v\nsecond %+v", first, second)
			}
		})
	}
}

// sameModel compares configs ignoring the order of lists Render sorts.
func sameModel(a, b *Config) bool {
	norm := func(c *Config) Config {
		cp := *c
		r, _ := Render(c, renderOpt)
		re, _ := Parse(r)
		cp.HomeDomains, cp.Validators, cp.History = re.Config.HomeDomains, re.Config.Validators, re.Config.History
		return cp
	}
	return reflect.DeepEqual(norm(a), norm(b))
}

func TestUnknownKeysKeptVerbatim(t *testing.T) {
	text := input(t, "adopted-unknown-keys.cfg")
	res := mustParse(t, text)
	var keys []string
	for _, e := range res.Config.Unknown {
		keys = append(keys, e.Key)
	}
	want := []string{"COMMANDS", "INVARIANT_CHECKS", "CATCHUP_RECENT", "WEIRD KEY", "[OVERLAY_TUNING]"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("unknown keys %q, want %q", keys, want)
	}
	out, err := Render(res.Config, renderOpt)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Config.Unknown {
		if !strings.Contains(string(out), e.Raw) {
			t.Errorf("%s not kept verbatim:\n%q", e.Key, e.Raw)
		}
	}
	// Multi-line arrays keep their inner comments; values are unchanged.
	var before, after map[string]any
	toml.Unmarshal(text, &before)
	toml.Unmarshal(out, &after)
	for _, k := range []string{"COMMANDS", "INVARIANT_CHECKS", "CATCHUP_RECENT", "WEIRD KEY", "OVERLAY_TUNING"} {
		if !reflect.DeepEqual(before[k], after[k]) {
			t.Errorf("%s: %v became %v", k, before[k], after[k])
		}
	}
	if res.Config.LogFilePath == nil || *res.Config.LogFilePath != "" {
		t.Error(`LOG_FILE_PATH="" not kept distinct from absent`)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	base := mustParse(t, input(t, "adopted-unknown-keys.cfg")).Config
	want, _ := Render(base, renderOpt)
	rng := rand.New(rand.NewPCG(1, 2))
	for range 50 {
		c := *base
		c.Validators = append([]Validator(nil), base.Validators...)
		c.HomeDomains = append([]HomeDomain(nil), base.HomeDomains...)
		c.History = append([]HistoryArchive(nil), base.History...)
		rng.Shuffle(len(c.Validators), func(i, j int) { c.Validators[i], c.Validators[j] = c.Validators[j], c.Validators[i] })
		rng.Shuffle(len(c.HomeDomains), func(i, j int) { c.HomeDomains[i], c.HomeDomains[j] = c.HomeDomains[j], c.HomeDomains[i] })
		rng.Shuffle(len(c.History), func(i, j int) { c.History[i], c.History[j] = c.History[j], c.History[i] })
		got, _ := Render(&c, renderOpt)
		if !bytes.Equal(got, want) {
			t.Fatalf("shuffled input changed the render:\n%s", Diff("want", "got", want, got))
		}
	}
}

func TestRenderRefusesPublicHTTPPort(t *testing.T) {
	c := testnetBasic()
	c.PublicHTTPPort = Ptr(true)
	if _, err := Render(c, renderOpt); !errors.Is(err, ErrPublicHTTPPort) {
		t.Errorf("err = %v", err)
	}
	if !hasRule(Validate(c, ValidateOptions{}), "public-http-port", Block) {
		t.Error("validation did not block PUBLIC_HTTP_PORT=true")
	}
}

func TestSeedMarkers(t *testing.T) {
	for _, s := range []Seed{
		{Mode: SeedKeep, PublicKey: "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H", Alias: "acme-a"},
		{Mode: SeedGenerate, Alias: "acme-b"},
		{Mode: SeedImport},
	} {
		got, err := parseSeed(s.SeedMarker())
		if err != nil || *got != s {
			t.Errorf("%q parsed as %+v, %v", s.SeedMarker(), got, err)
		}
		red, err := parseSeed(s.Redacted())
		if err != nil || red.PublicKey != s.PublicKey || red.Alias != s.Alias || red.Mode != SeedKeep {
			t.Errorf("%q parsed as %+v, %v", s.Redacted(), red, err)
		}
	}
	if s := (Seed{Mode: SeedKeep, PublicKey: "G1", Alias: "a"}).SeedMarker(); s != "<auriga-seed:keep:G1> a" {
		t.Errorf("marker %q", s)
	}
}

func TestParseErrors(t *testing.T) {
	var pe *ParseError
	_, err := Parse(input(t, "malformed.cfg"))
	if !errors.As(err, &pe) || pe.Line != 2 || pe.Column < 1 {
		t.Errorf("malformed: %v", err)
	}
	_, err = Parse([]byte("NETWORK_PASSPHRASE=\"x\"\n\nHTTP_PORT=\"abc\"\n"))
	if !errors.As(err, &pe) || pe.Line != 3 || pe.Column != 1 || pe.Key != "HTTP_PORT" {
		t.Errorf("wrong type: %v", err)
	}
	_, err = Parse(input(t, "plain-seed.cfg"))
	if err == nil || !strings.Contains(err.Error(), "plain secret seed") || strings.Contains(err.Error(), "SDHO") {
		t.Errorf("plain seed: %v", err)
	}
	_, err = Parse([]byte(`KNOWN_PEERS=[1, 2]`))
	if !errors.As(err, &pe) || pe.Key != "KNOWN_PEERS" {
		t.Errorf("peers: %v", err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		present bool
		text    string
		want    Class
		reason  string
	}{
		{"absent", false, "", Unconfigured, ""},
		{"empty", true, "", Unconfigured, ""},
		{"comments only", true, "# nothing yet\n\n", Unconfigured, ""},
		{"sdf example", true, string(input(t, "sdf-pubnet-validator-full.cfg")), Adoptable, ""},
		{"package example", true, string(input(t, "package-testnet-validator.redacted.cfg")), Adoptable, ""},
		{"adopted", true, string(input(t, "adopted-unknown-keys.cfg")), Adoptable, ""},
		{"legacy quorum set", true, string(input(t, "legacy-quorum-set.cfg")), ReadOnly, "legacy [QUORUM_SET]"},
		{"malformed", true, string(input(t, "malformed.cfg")), ReadOnly, "does not parse: line 2"},
		{"dotted history", true, "NETWORK_PASSPHRASE=\"x\"\nHISTORY.a.get=\"cp {0} {1}\"\n", ReadOnly, "dotted key"},
		{"unknown validator key", true, "[[VALIDATORS]]\nNAME=\"a\"\nWEIGHT=3\n", ReadOnly, "[[VALIDATORS]] has key WEIGHT"},
		{"inline history", true, "HISTORY={a={get=\"x\"}}\n", ReadOnly, "inline value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Classify(tt.present, []byte(tt.text))
			if c.Class != tt.want {
				t.Fatalf("class %v (%v), want %v", c.Class, c.Reasons, tt.want)
			}
			if tt.reason != "" && !strings.Contains(strings.Join(c.Reasons, "; "), tt.reason) {
				t.Errorf("reasons %q lack %q", c.Reasons, tt.reason)
			}
		})
	}
}

func hasRule(issues []Issue, rule string, sev Severity) bool {
	for _, i := range issues {
		if i.Rule == rule && i.Severity == sev {
			return true
		}
	}
	return false
}

func TestValidateRules(t *testing.T) {
	tests := []struct {
		rule string
		sev  Severity
		mod  func(*Config)
		opt  ValidateOptions
	}{
		{"network-passphrase", Block, func(c *Config) { c.NetworkPassphrase = "" }, ValidateOptions{}},
		{"validator-seed", Block, func(c *Config) { c.NodeSeed = nil }, ValidateOptions{}},
		{"home-domain", Block, func(c *Config) { c.NodeHomeDomain = "not a domain" }, ValidateOptions{}},
		{"port-range", Block, func(c *Config) { c.HTTPPort = Ptr(70000) }, ValidateOptions{}},
		{"port-clash", Block, func(c *Config) { c.PeerPort = Ptr(11626) }, ValidateOptions{}},
		{"database-scheme", Block, func(c *Config) { c.Database = "mysql://x" }, ValidateOptions{}},
		{"database-missing", Warn, func(c *Config) { c.Database = "" }, ValidateOptions{}},
		{"peer-address", Block, func(c *Config) { c.KnownPeers = []string{"a b"} }, ValidateOptions{}},
		{"peer-key", Block, func(c *Config) { c.PreferredPeerKeys = []string{"has space"} }, ValidateOptions{}},
		{"quality", Block, func(c *Config) { c.HomeDomains[0].Quality = "SUPREME" }, ValidateOptions{}},
		{"duplicate-home-domain", Block, func(c *Config) { c.HomeDomains[1].HomeDomain = c.HomeDomains[0].HomeDomain }, ValidateOptions{}},
		{"duplicate-name", Block, func(c *Config) { c.Validators[1].Name = c.Validators[0].Name }, ValidateOptions{}},
		{"duplicate-key", Block, func(c *Config) { c.Validators[1].PublicKey = c.Validators[0].PublicKey }, ValidateOptions{}},
		{"public-key", Block, func(c *Config) { c.Validators[0].PublicKey = "GNOPE" }, ValidateOptions{}},
		{"name-is-self", Block, func(c *Config) { c.Validators[1].Name = "acme-a" }, ValidateOptions{}},
		{"validator-home-domain", Block, func(c *Config) { c.Validators[0].HomeDomain = "" }, ValidateOptions{}},
		{"validator-address", Block, func(c *Config) { c.Validators[1].Address = "core b" }, ValidateOptions{}},
		{"history-template", Block, func(c *Config) { c.Validators[0].History = "curl x" }, ValidateOptions{}},
		{"history-template", Block, func(c *Config) { c.History = []HistoryArchive{{Name: "a", Get: "cp {0} x"}} }, ValidateOptions{}},
		{"history-template", Block, func(c *Config) { c.History = []HistoryArchive{{Name: "a", Get: "cp {0} {1}", Put: "cp {0} x"}} }, ValidateOptions{}},
		{"history-template", Block, func(c *Config) {
			c.History = []HistoryArchive{{Name: "a", Get: "cp {0} {1}", Put: "cp {0} {1}", Mkdir: "mkdir -p x"}}
		}, ValidateOptions{}},
		{"history-get", Block, func(c *Config) { c.History = []HistoryArchive{{Name: "a", Put: "cp {0} {1}"}} }, ValidateOptions{}},
		{"archive-name", Block, func(c *Config) { c.History = []HistoryArchive{{Name: "a b", Get: "cp {0} {1}"}} }, ValidateOptions{}},
		{"relative-path", Warn, func(c *Config) { c.LogFilePath = nil }, ValidateOptions{ServiceWorkDir: "/"}},
		{"relative-path", Warn, func(c *Config) { c.BucketDirPath = "buckets" }, ValidateOptions{ServiceWorkDir: "/"}},
		{"relative-path", Warn, func(c *Config) { c.Database = "sqlite3://stellar.db" }, ValidateOptions{ServiceWorkDir: "/"}},
	}
	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			c := testnetBasic()
			tt.mod(c)
			if issues := Validate(c, tt.opt); !hasRule(issues, tt.rule, tt.sev) {
				t.Errorf("rule %s (%v) not raised; got %v", tt.rule, tt.sev, issues)
			}
		})
	}
	// Relative paths are fine when the unit sets a working directory.
	c := testnetBasic()
	c.BucketDirPath, c.LogFilePath = "buckets", nil
	if hasRule(Validate(c, ValidateOptions{ServiceWorkDir: "/var/lib/stellar"}), "relative-path", Warn) {
		t.Error("relative path warned although the unit has a working directory")
	}
}

func TestDiffAndAdoptionNotes(t *testing.T) {
	text := input(t, "adopted-unknown-keys.cfg")
	res := mustParse(t, text)
	out, _ := Render(res.Config, renderOpt)
	d := Diff("current", "auriga", text, out)
	if !strings.HasPrefix(d, "--- current\n+++ auriga\n") || !strings.Contains(d, "+# stellar-core configuration, managed by auriga") {
		t.Errorf("diff:\n%s", d)
	}
	if Diff("a", "b", out, out) != "" {
		t.Error("diff of equal files is not empty")
	}
	notes := strings.Join(AdoptionNotes(res), "\n")
	for _, want := range []string{"3 comment lines will be dropped", "fixed order", "5 other settings are kept unchanged: COMMANDS"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
}

func FuzzParseRender(f *testing.F) {
	for _, name := range append(adoptable, "legacy-quorum-set.cfg", "malformed.cfg") {
		b, _ := os.ReadFile(filepath.Join("testdata", "input", name))
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		res, err := Parse(text)
		if err != nil {
			return
		}
		out, err := Render(res.Config, renderOpt)
		if err != nil {
			return // PUBLIC_HTTP_PORT=true
		}
		res2, err := Parse(out)
		if err != nil {
			t.Fatalf("render does not parse: %v\ninput:\n%s\nrender:\n%s", err, text, out)
		}
		out2, err := Render(res2.Config, renderOpt)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("render not stable (%v):\n%s", err, Diff("1", "2", out, out2))
		}
	})
}
