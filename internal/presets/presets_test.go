package presets

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

var update = flag.Bool("update", false, "regenerate data/*.toml from testdata/upstream")

// Upstream snapshots the embedded presets come from.
var snapshots = map[Network]struct{ file, date string }{
	// stellar/stellar-core docs/stellar-core_testnet_validator.cfg, commit eaf9558.
	Testnet: {"stellar-core_testnet_validator.cfg", "2024-12-13"},
	// stellar/packages docs/examples/pubnet-validator-full/stellar-core.cfg, commit e3a92b9.
	Pubnet: {"pubnet-validator-full.cfg", "2026-08-25"},
}

// TestEmbeddedMatchesUpstream regenerates (with -update) or checks the
// embedded presets against the upstream copies in testdata, using the same
// Extract the refresh action uses.
func TestEmbeddedMatchesUpstream(t *testing.T) {
	for n, s := range snapshots {
		t.Run(string(n), func(t *testing.T) {
			up, err := os.ReadFile(filepath.Join("testdata", "upstream", s.file))
			if err != nil {
				t.Fatal(err)
			}
			p, err := Extract(n, SourceURL(n), s.date, up)
			if err != nil {
				t.Fatal(err)
			}
			want, err := p.Encode()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("data", string(n)+".toml")
			if *update {
				if err := os.WriteFile(path, want, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, want) {
				t.Errorf("%s is out of date; run go test ./internal/presets -update", path)
			}
		})
	}
}

func TestLoadEmbedded(t *testing.T) {
	tn, err := Load("", Testnet)
	if err != nil {
		t.Fatal(err)
	}
	if len(tn.HomeDomains) != 1 || tn.HomeDomains[0].HomeDomain != "testnet.stellar.org" || tn.HomeDomains[0].Quality != stellarcfg.High ||
		len(tn.Validators) != 3 || tn.Snapshot != "2024-12-13" || tn.Overridden {
		t.Errorf("testnet %+v", tn)
	}
	for _, v := range tn.Validators {
		if !strings.Contains(v.History, "{0}") {
			t.Errorf("%s has no HISTORY template", v.Name)
		}
	}
	pn, err := Load("", Pubnet)
	if err != nil {
		t.Fatal(err)
	}
	if len(pn.HomeDomains) != 10 || len(pn.Validators) != 30 || !strings.Contains(pn.Summary(), "10 orgs, 30 validators (built in, snapshot 2026-08-25)") {
		t.Errorf("pubnet %s", pn.Summary())
	}
	names := map[string]bool{}
	for _, v := range pn.Validators {
		names[v.Name] = true
	}
	if !names["Boötes"] || !names["LOBSTR 1 (Europe)"] {
		t.Error("validator names with spaces or accents lost")
	}
	if _, err := Load("", "futurenet"); err == nil {
		t.Error("futurenet accepted")
	}
}

func TestExtractIgnoresEverythingElse(t *testing.T) {
	up := []byte(`NODE_SEED="SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4 me"
NETWORK_PASSPHRASE="x"
[[HOME_DOMAINS]]
HOME_DOMAIN="me.example"
QUALITY="MEDIUM"
[[HOME_DOMAINS]]
HOME_DOMAIN="them.example"
QUALITY="HIGH"
[[VALIDATORS]]
NAME="t1"
HOME_DOMAIN="them.example"
PUBLIC_KEY="GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
HISTORY="curl -sf https://h/{0} -o {1}"
`)
	p, err := Extract(Testnet, "https://example/x.cfg", "2026-11-16", up)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Encode()
	if strings.Contains(string(b), "SDHO") || strings.Contains(string(b), "me.example") || len(p.HomeDomains) != 1 {
		t.Errorf("extracted:\n%s", b)
	}
	if _, err := Extract(Testnet, "u", "d", []byte("[[VALIDATORS]]\nNAME=\"x\"\nPUBLIC_KEY=\"GNOPE\"\n")); err == nil {
		t.Error("invalid validator accepted")
	}
}

func TestOverride(t *testing.T) {
	dir := t.TempDir()
	p, _ := Load("", Testnet)
	p.Snapshot = "2026-11-16"
	p.Validators = p.Validators[:3]
	if err := SaveOverride(dir, p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "presets", "testnet.toml"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	got, err := Load(dir, Testnet)
	if err != nil || !got.Overridden || got.Snapshot != "2026-11-16" || !strings.Contains(got.Summary(), "refreshed") {
		t.Errorf("%+v %v", got, err)
	}
	if other, _ := Load(dir, Pubnet); other.Overridden {
		t.Error("pubnet affected by a testnet override")
	}
	os.WriteFile(filepath.Join(dir, "presets", "pubnet.toml"), []byte("garbage = ="), 0o600)
	if _, err := Load(dir, Pubnet); err == nil {
		t.Error("broken override accepted")
	}
}
