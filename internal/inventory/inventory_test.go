package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const addr = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"

func TestLoadMissingIsEmpty(t *testing.T) {
	inv, err := Load(t.TempDir())
	if err != nil || inv.Version != CurrentVersion || len(inv.Hosts) != 0 {
		t.Fatalf("%+v %v", inv, err)
	}
}

func TestRoundTripAndModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auriga")
	inv := New()
	for _, a := range []string{"acme-a", "acme-b", "acme-c"} {
		if added, err := inv.AddHost(a); err != nil || !added {
			t.Fatal(a, err)
		}
	}
	if added, _ := inv.AddHost("acme-a"); added {
		t.Error("duplicate added")
	}
	inv.Host("acme-a").PublicKey = addr
	inv.GroupByHomeDomain("acme-a", "acme.org", NetworkPubnet)
	inv.GroupByHomeDomain("acme-b", "acme.org", NetworkPubnet)
	if err := inv.Save(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v %v", fi, err)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o", di.Mode().Perm())
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hosts) != 3 || len(got.Orgs) != 1 || got.Orgs[0].Network != NetworkPubnet {
		t.Fatalf("%+v", got)
	}
	if h := got.Host("acme-a"); h.Org != "acme.org" || h.PublicKey != addr || h.ConfigPath != "/etc/stellar/stellar-core.cfg" || h.Service != "stellar-core" {
		t.Errorf("%+v", h)
	}
	b, _ := os.ReadFile(Path(dir))
	if !strings.Contains(string(b), "version = 1") || !strings.Contains(string(b), "[[hosts]]") {
		t.Errorf("file:\n%s", b)
	}
}

func TestRemoveHostPrunesOrg(t *testing.T) {
	inv := New()
	inv.AddHost("a")
	inv.AddHost("b")
	inv.GroupByHomeDomain("a", "x.org", "")
	inv.RemoveHost("a")
	if len(inv.Orgs) != 0 || inv.Host("a") != nil || inv.Host("b") == nil {
		t.Errorf("%+v", inv)
	}
}

func TestManualOrgWins(t *testing.T) {
	inv := New()
	inv.AddHost("a")
	inv.Orgs = append(inv.Orgs, Org{Name: "mine", HomeDomain: "mine.org"})
	if err := inv.AssignOrg("a", "mine"); err != nil {
		t.Fatal(err)
	}
	inv.GroupByHomeDomain("a", "other.org", "")
	if inv.Host("a").Org != "mine" {
		t.Error("automatic grouping overrode a manual org")
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"version":      "version = 2\n",
		"unknown key":  "version = 1\nsurprise = true\n",
		"bad alias":    "version = 1\n[[hosts]]\nalias = \"a b\"\nconfig_path = \"/x\"\nservice = \"s\"\n",
		"bad path":     "version = 1\n[[hosts]]\nalias = \"a\"\nconfig_path = \"/x;rm\"\nservice = \"s\"\n",
		"missing org":  "version = 1\n[[hosts]]\nalias = \"a\"\norg = \"nope\"\nconfig_path = \"/x\"\nservice = \"s\"\n",
		"bad network":  "version = 1\n[[orgs]]\nname = \"o\"\nnetwork = \"futurenet\"\n",
		"bad key":      "version = 1\n[[hosts]]\nalias = \"a\"\npublic_key = \"GNOPE\"\nconfig_path = \"/x\"\nservice = \"s\"\n",
		"syntax error": "version = = 1\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestLoadSpecExample(t *testing.T) {
	// The example from SPEC.md "Local state and files".
	body := `version = 1

[[orgs]]
name        = "acme"
home_domain = "acme.org"
network     = "pubnet"

[[hosts]]
alias          = "acme-a"          # Host alias in ~/.ssh/config
org            = "acme"
node_name      = "acme-a"
public_key     = "` + addr + `"
config_path    = "/etc/stellar/stellar-core.cfg"
service        = "stellar-core"
http_port      = 11626
public_address = "core-a.acme.org:11625"
archive_name   = "acme-a"
archive_url    = "https://history-a.acme.org"
`
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h := inv.Host("acme-a"); h.ArchiveURL != "https://history-a.acme.org" || h.HTTPPort != 11626 {
		t.Errorf("%+v", h)
	}
}

func TestNetworkFromPassphrase(t *testing.T) {
	if NetworkFromPassphrase("Test SDF Network ; September 2015") != NetworkTestnet ||
		NetworkFromPassphrase("Public Global Stellar Network ; September 2015") != NetworkPubnet ||
		NetworkFromPassphrase("Standalone Network ; February 2017") != "" {
		t.Error("mapping wrong")
	}
}

// The test network's name is saved like the others; the inventory loads
// later without the flag.
func TestPrivateNetworkSaves(t *testing.T) {
	dir := t.TempDir()
	inv := New()
	inv.AddHost("it-a")
	SetTestNetwork("Auriga Integration Network ; November 2026")
	t.Cleanup(func() { SetTestNetwork("") })
	inv.GroupByHomeDomain("it-a", "itest.example", NetworkFromPassphrase("Auriga Integration Network ; November 2026"))
	if err := inv.Save(dir); err != nil {
		t.Fatal(err)
	}
	SetTestNetwork("")
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
}
