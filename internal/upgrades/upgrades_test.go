package upgrades

import (
	"strings"
	"testing"
	"time"
)

var bell = time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)

func TestParseArmingStrings(t *testing.T) {
	for _, in := range []string{
		"upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30",
		"  upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30\n",
		`curl "http://localhost:11626/upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30"`,
		"stellar-core http-command 'upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30'",
		"mode=set&upgradetime=2026-11-23T17%3A00%3A00Z&protocolversion=30",
		"upgradetime=2026-11-23T17:00:00Z&ProtocolVersion=30",
	} {
		p, err := Parse(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if v, _ := p.Get("protocolversion"); !p.Time.Equal(bell) || v != 30 || len(p.Values) != 1 {
			t.Errorf("%q: %+v", in, p)
		}
	}
}

func TestParseAllFieldsAndRoundTrip(t *testing.T) {
	key := strings.Repeat("A", 86) + "=="
	in := "upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30&basefee=200&basereserve=5000000" +
		"&maxtxsetsize=1000&maxsorobantxsetsize=100&configupgradesetkey=" + key + "&nominationtimeoutlimit=4&expirationminutes=60"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Settings() != 7 {
		t.Errorf("settings %d", p.Settings())
	}
	if p.String() != strings.Replace(in, "&configupgradesetkey="+key, "", 1)+"&configupgradesetkey="+key {
		t.Errorf("string %s", p.String())
	}
	again, err := Parse(p.String())
	if err != nil || again.String() != p.String() {
		t.Errorf("round trip %v %s", err, again.String())
	}
	q := p.Query()
	if q.Get("mode") != "set" || q.Get("upgradetime") != "2026-11-23T17:00:00Z" || q.Get("basefee") != "200" ||
		!strings.Contains(q.Encode(), "upgradetime=2026-11-23T17%3A00%3A00Z") {
		t.Errorf("query %s", q.Encode())
	}
	if CheckKey(key) != nil {
		t.Error("valid key refused")
	}
}

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		"":                                      "nothing to parse",
		"upgrades?mode=clear":                   "only mode=set",
		"upgrades?mode=set&upgradetime=soon":    "not a UTC time",
		"upgrades?upgradetime=2026-11-23 17:00": "not a UTC time",
		"upgrades?basefee=4294967296":           "outside uint32",
		"upgrades?basefee=-1":                   "not a whole number",
		"upgrades?basefee=1&basefee=2":          "appears twice",
		"upgrades?banpeer=x":                    "unknown setting",
		"upgrades?basefee":                      "has no value",
	} {
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
	for key, want := range map[string]string{"not base64!": "not valid base64", "AAAA": "decodes to 3 bytes"} {
		if err := CheckKey(key); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("key %q: %v", key, err)
		}
	}
}

func TestFromScheduledAndMatches(t *testing.T) {
	p, _ := Parse("upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&basefee=200")
	s := FromScheduled(map[string]string{"fee": "200", "time": "1795453200"})
	if !p.Matches(s) {
		t.Errorf("%+v does not match %+v", p, s)
	}
	if p.Matches(FromScheduled(map[string]string{"fee": "100", "time": "1795453200"})) || p.Matches(FromScheduled(map[string]string{})) {
		t.Error("mismatch accepted")
	}
}

func host(alias string, proto, ledger int) Host {
	return Host{Alias: alias, Org: "acme", Network: "pubnet", Build: "v30.0.0", ProtocolVersion: proto, LedgerVersion: ledger}
}

func rules(issues []Issue) map[string]bool {
	out := map[string]bool{}
	for _, i := range issues {
		r := i.Rule
		if i.Block {
			r += "!"
		}
		out[r] = true
	}
	return out
}

func TestChecks(t *testing.T) {
	now := bell.Add(-48 * time.Hour)
	ok, _ := Parse("upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&protocolversion=30")
	if is := Check(ok, "pubnet", []Host{host("a", 30, 29), host("b", 30, 29)}, now); len(is) != 0 {
		t.Fatalf("clean request: %v", is)
	}
	for _, tc := range []struct {
		name  string
		p     string
		net   string
		hosts []Host
		want  string
	}{
		{"unsupported", "upgradetime=2026-11-23T17:00:00Z&protocolversion=31", "pubnet", []Host{host("a", 30, 29)}, "protocol-unsupported!"},
		{"not higher", "upgradetime=2026-11-23T17:00:00Z&protocolversion=29", "pubnet", []Host{host("a", 30, 29)}, "protocol-not-higher!"},
		{"no info yet", "upgradetime=2026-11-23T17:00:00Z&protocolversion=30", "pubnet", []Host{host("a", 0, 0)}, "protocol-unknown!"},
		{"no time", "basefee=100", "pubnet", []Host{host("a", 30, 29)}, "time!"},
		{"nothing", "upgradetime=2026-11-23T17:00:00Z&expirationminutes=30", "pubnet", []Host{host("a", 30, 29)}, "nothing!"},
		{"bad key", "upgradetime=2026-11-23T17:00:00Z&configupgradesetkey=AAAA", "pubnet", []Host{host("a", 30, 29)}, "key!"},
		{"network", "upgradetime=2026-11-23T17:00:00Z&basefee=100", "testnet", []Host{host("a", 30, 29)}, "network!"},
		{"no hosts", "upgradetime=2026-11-23T17:00:00Z&basefee=100", "pubnet", nil, "no-hosts!"},
		{"past", "upgradetime=2026-11-20T17:00:00Z&basefee=100", "pubnet", []Host{host("a", 30, 29)}, "time-past"},
		{"far", "upgradetime=2027-01-23T17:00:00Z&basefee=100", "pubnet", []Host{host("a", 30, 29)}, "time-far"},
		{"builds", "upgradetime=2026-11-23T17:00:00Z&basefee=100", "pubnet", []Host{host("a", 30, 29), {Alias: "b", Org: "acme", Network: "pubnet", Build: "v29.1.0"}}, "builds-differ"},
		{"armed", "upgradetime=2026-11-23T17:00:00Z&basefee=100", "pubnet", []Host{host("a", 30, 29), {Alias: "b", Org: "acme", Network: "pubnet", Build: "v30.0.0", Armed: "Armed with network upgrades: basefee=200"}}, "armed-differently"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse(tc.p)
			if err != nil {
				t.Fatal(err)
			}
			is := Check(p, tc.net, tc.hosts, now)
			if !rules(is)[tc.want] {
				t.Errorf("want %s in %v", tc.want, is)
			}
			if Blocking(is) != strings.HasSuffix(tc.want, "!") {
				t.Errorf("blocking %v for %v", Blocking(is), is)
			}
		})
	}
}
