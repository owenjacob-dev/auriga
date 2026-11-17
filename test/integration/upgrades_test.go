//go:build integration

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/remote"
	"github.com/owenjacob-dev/auriga/internal/upgrades"
)

func infoOf(t *testing.T, c *corehttp.Client) corehttp.Info {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, _, err := c.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return r.Info
}

// armAll arms p on every target in parallel, as the upgrades screen does,
// and confirms each with upgrades?mode=get and the info status line.
func armAll(t *testing.T, b *app.LiveBackend, p upgrades.Params) {
	t.Helper()
	var wg sync.WaitGroup
	for _, a := range targets {
		wg.Go(func() {
			c := b.AdminClient(a, 11626, remote.DefaultConfigPath, remote.DefaultService)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := c.ArmUpgrades(ctx, p.Query()); err != nil {
				t.Errorf("%s: arm: %v", a, err)
				return
			}
			u, _, err := c.Upgrades(ctx)
			if err != nil || !p.Matches(upgrades.FromScheduled(u.Scheduled())) {
				t.Errorf("%s: mode=get does not show %s: %v %v", a, p, u, err)
			}
			if armed := infoOf(t, c).Armed(); armed == "" {
				t.Errorf("%s: info does not report the vote", a)
			}
		})
	}
	wg.Wait()
}

// waitAll polls info on every host, as the dashboard does, until ok.
func waitAll(t *testing.T, b *app.LiveBackend, what string, ok func(corehttp.Info) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for _, a := range targets {
		c := b.AdminClient(a, 11626, remote.DefaultConfigPath, remote.DefaultService)
		for {
			i := infoOf(t, c)
			if ok(i) {
				t.Logf("%s: %s at ledger %d", a, what, i.Ledger.Num)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s never happened: ledger %+v status %v", a, what, i.Ledger, i.Status)
			}
			time.Sleep(3 * time.Second)
		}
	}
}

// TestArmUpgradesOnRealHosts is M6's acceptance: arming a basefee change
// on the three validators (it-b through the http-command fallback), which
// the polled info then shows; then a protocol upgrade, the Soroban
// settings it enables, the checks against real info, and clearing.
func TestArmUpgradesOnRealHosts(t *testing.T) {
	b := liveBackend(t)
	now := time.Now().UTC().Truncate(time.Second)
	before := infoOf(t, b.AdminClient("it-a", 11626, remote.DefaultConfigPath, remote.DefaultService))

	// Checks against what the hosts report: a protocol beyond the build.
	var hosts []upgrades.Host
	for _, a := range targets {
		i := infoOf(t, b.AdminClient(a, 11626, remote.DefaultConfigPath, remote.DefaultService))
		hosts = append(hosts, upgrades.Host{Alias: a, Org: "itest", Network: "private", Build: i.Build,
			ProtocolVersion: i.ProtocolVersion, LedgerVersion: i.Ledger.Version, Armed: i.Armed()})
	}
	tooNew, _ := upgrades.Parse("upgradetime=" + now.Add(time.Hour).Format(upgrades.TimeLayout) + "&protocolversion=31")
	if is := upgrades.Check(tooNew, "private", hosts, now); !upgrades.Blocking(is) || !strings.Contains(is[0].Message, "supports protocol 30 at most") {
		t.Errorf("protocol 31 not blocked: %v", is)
	}
	// And core refuses it anyway, with an error we report.
	err := b.AdminClient("it-a", 11626, remote.DefaultConfigPath, remote.DefaultService).ArmUpgrades(context.Background(), tooNew.Query())
	if err == nil || !strings.Contains(err.Error(), "supported is up to 30") {
		t.Errorf("core's refusal not reported: %v", err)
	}

	// The acceptance: a basefee change, which the polled info then shows.
	fee := uint32(before.Ledger.BaseFee + 150)
	p := upgrades.Params{Time: now.Add(20 * time.Second), Values: map[string]uint32{"basefee": fee}}
	if is := upgrades.Check(p, "private", hosts, now); upgrades.Blocking(is) {
		t.Fatalf("basefee change blocked: %v", is)
	}
	armAll(t, b, p)
	waitAll(t, b, "base fee "+p.String(), func(i corehttp.Info) bool { return i.Ledger.BaseFee == int64(fee) })

	// A protocol upgrade (the stack's network starts at 0), which turns on
	// Soroban and its settings.
	if before.Ledger.Version < before.ProtocolVersion {
		proto := upgrades.Params{Time: time.Now().UTC().Truncate(time.Second).Add(20 * time.Second),
			Values: map[string]uint32{"protocolversion": uint32(before.ProtocolVersion)}}
		armAll(t, b, proto)
		waitAll(t, b, "protocol upgrade", func(i corehttp.Info) bool { return i.Ledger.Version == before.ProtocolVersion })
	}
	c := b.AdminClient("it-b", 11626, remote.DefaultConfigPath, remote.DefaultService) // http-command
	settings, err := c.SorobanInfo(context.Background())
	if err != nil || !strings.Contains(string(settings), "CONFIG_SETTING_CONTRACT_MAX_SIZE_BYTES") {
		t.Fatalf("sorobaninfo: %v", err)
	}
	if _, err := c.ProposedSettings(context.Background(), strings.Repeat("A", 86)+"=="); err == nil || !strings.Contains(err.Error(), "missing or invalid") {
		t.Errorf("an unknown proposal was not refused: %v", err)
	}

	// expirationminutes (SPEC.md: verify which builds accept it): core 30
	// accepts it and reports it back; then clear.
	far := upgrades.Params{Time: now.Add(24 * time.Hour), Values: map[string]uint32{"basereserve": 6000000, "expirationminutes": 30}}
	armAll(t, b, far)
	for _, a := range targets {
		c := b.AdminClient(a, 11626, remote.DefaultConfigPath, remote.DefaultService)
		if err := c.ClearUpgrades(context.Background()); err != nil {
			t.Fatalf("%s: clear: %v", a, err)
		}
		u, _, err := c.Upgrades(context.Background())
		if err != nil || len(u.Scheduled()) != 0 || infoOf(t, c).Armed() != "" {
			t.Errorf("%s: still armed after clear: %v %v", a, u.Scheduled(), err)
		}
	}
}
