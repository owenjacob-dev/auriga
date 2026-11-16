//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/presets"
	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
	"github.com/owenjacob-dev/auriga/internal/ui/wizard"
)

var seedMarker = regexp.MustCompile(`"<auriga-seed:[a-z]+:(G[A-Z2-7]{55})>( [^"]*)?"`)

// TestWizardRendersLoadInStellarCore loads the configs the wizard renders
// for both fixture orgs into stellar-core 30 (new-db parses the config,
// generates the quorum set and checks its safety). Seed markers are
// replaced with the public test-vector seeds, as the stage helper will on
// a host.
func TestWizardRendersLoadInStellarCore(t *testing.T) {
	seeds := map[string]string{AddressA: SeedA, AddressB: SeedB, AddressC: SeedC}
	keys := map[string]string{"acme-a": AddressA, "acme-b": AddressB, "acme-c": AddressC}
	dir := t.TempDir()
	for _, org := range []struct {
		name, network, nodeType, quality string
		preset                           presets.Network
	}{
		{"testnet-basic", "testnet", wizard.TypeBasic, "MEDIUM", presets.Testnet},
		{"pubnet-full", "pubnet", wizard.TypeFull, "HIGH", presets.Pubnet},
	} {
		d := wizard.NewDraft()
		d.OrgName, d.HomeDomain, d.Network, d.NodeType = "acme", "acme.example", org.network, org.nodeType
		d.Hosts = []string{"acme-a", "acme-b", "acme-c"}
		d.Quorum.OwnQuality, d.Quorum.UnsafeConsent = org.quality, true
		facts := map[string]wizard.HostFacts{}
		for _, h := range d.Hosts {
			d.Keys[h] = wizard.KeyInput{Mode: wizard.KeyImport, Alias: h, Address: h + ".acme.example:11625", ImportedPublicKey: keys[h]}
			d.Advanced[h] = wizard.AdvancedInput{Database: "sqlite3:///tmp/" + org.name + "-" + h + ".db", BucketDir: "/tmp/" + org.name + "-" + h + "-buckets", LogFile: ""}
			if wizard.Publishes(org.nodeType) {
				d.Archives[h] = stellarcfg.ArchiveSpec{Name: h, Backend: stellarcfg.BackendNginx, URL: "https://history-" + h + ".acme.example", Dir: "/tmp/history-" + h}
			}
			facts[h] = wizard.HostFacts{ServiceWorkDir: "/", ArchiveExists: "no"}
		}
		p, err := presets.Load("", org.preset)
		if err != nil {
			t.Fatal(err)
		}
		if org.preset == presets.Pubnet {
			// acme-c trusts only the first three Tier 1 orgs (per-host override).
			keep := map[string]bool{}
			for _, h := range p.HomeDomains[:3] {
				keep[h.HomeDomain] = true
			}
			var vals []stellarcfg.Validator
			for _, v := range p.Validators {
				if keep[v.HomeDomain] {
					vals = append(vals, v)
				}
			}
			a := d.Advanced["acme-c"]
			a.QuorumOverride, a.QuorumEntries = true, stellarcfg.RenderQuorumEntries(p.HomeDomains[:3], vals)
			d.Advanced["acme-c"] = a
		}
		plan := wizard.BuildPlan(wizard.Input{Draft: d, Facts: facts, Preset: p, Render: stellarcfg.RenderOptions{Version: "itest", Time: time.Unix(0, 0)}})
		if plan.Blocking(true) {
			t.Fatalf("%s: blocking issues %v", org.name, plan.Issues)
		}
		if org.preset == presets.Pubnet && (!plan.Hosts[2].OwnQuorum || len(plan.Hosts[2].Config.Validators) != 11) {
			t.Fatalf("override not applied: %d validators", len(plan.Hosts[2].Config.Validators))
		}
		for _, hp := range plan.Hosts {
			text := seedMarker.ReplaceAllStringFunc(string(hp.Rendered), func(m string) string {
				sub := seedMarker.FindStringSubmatch(m)
				return `"` + seeds[sub[1]] + sub[2] + `"`
			})
			if strings.Contains(text, "auriga-seed") {
				t.Fatalf("seed marker left in %s/%s", org.name, hp.Alias)
			}
			if err := os.WriteFile(filepath.Join(dir, org.name+"-"+hp.Alias+".cfg"), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	script := `for f in /cfg/*.cfg; do
  if stellar-core --conf "$f" new-db >/tmp/out 2>&1; then echo "$(basename "$f") accepted"; else echo "$(basename "$f") REFUSED: $(grep -m1 FATAL /tmp/out)"; fi
done`
	out, err := exec.Command("docker", "run", "--rm", "--platform", "linux/amd64", "-v", dir+":/cfg:ro", "--entrypoint", "sh",
		"auriga-itest-host:latest", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Logf("\n%s", out)
	if strings.Contains(string(out), "REFUSED") || strings.Count(string(out), "accepted") != 6 {
		t.Errorf("stellar-core refused a wizard render:\n%s", out)
	}
}
