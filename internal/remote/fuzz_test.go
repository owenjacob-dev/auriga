package remote

import (
	"net/url"
	"strings"
	"testing"
)

// shellSpecial are characters that mean something to sh outside quotes. No
// validated argument may carry one (the quoting already makes them inert;
// the allowlist is the second line of defence). '&' and '=' are allowed in
// admin queries, '+', '/' and '=' in base64, '~' and ':' in URLs.
const shellSpecial = "'\"`$\\;|<>(){}[]*?!#\t\n\r "

func FuzzArgValidate(f *testing.F) {
	for _, s := range []string{"acme-a", "/etc/stellar/stellar-core.cfg", "11626", "keep",
		"https://history.example.org/a", "QUJD", "info", "upgrades?mode=set&upgradetime=2026-11-23T17%3A00%3A00Z",
		"upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z", "/etc/../x", "$(id)", "a b", "info?x='"} {
		f.Add(s)
	}
	kinds := []ArgKind{ArgAlias, ArgName, ArgPath, ArgPort, ArgWord, ArgURL, ArgBase64, ArgAdmin, ArgAdminRaw}
	f.Fuzz(func(t *testing.T, s string) {
		for _, k := range kinds {
			if (Arg{k, s}).Validate() != nil {
				continue
			}
			check := s
			if k == ArgAdmin || k == ArgAdminRaw {
				_, check, _ = strings.Cut(s, "?") // the one '?' before the query
			}
			if strings.ContainsAny(check, shellSpecial) {
				t.Fatalf("kind %d accepted %q, which holds a shell character", k, s)
			}
			for _, r := range s {
				if r < 0x21 || r > 0x7e {
					t.Fatalf("kind %d accepted %q, which holds a non-printing or non-ASCII rune", k, s)
				}
			}
			if k == ArgPath && (strings.Contains(s, "/../") || strings.HasSuffix(s, "/..")) {
				t.Fatalf("path %q escapes with ..", s)
			}
			if k == ArgAdmin || k == ArgAdminRaw {
				name, _, _ := strings.Cut(s, "?")
				switch name {
				case "info", "quorum", "peers", "upgrades", "sorobaninfo", "dumpproposedsettings":
				default:
					t.Fatalf("admin command %q is outside the six allowed", s)
				}
			}
		}
	})
}

// FuzzRawCommand checks that decoding a query for http-command keeps its
// parameters: a decoded value must never add, drop or rename one.
func FuzzRawCommand(f *testing.F) {
	f.Add("upgradetime", "2026-11-23T17:00:00Z")
	f.Add("configupgradesetkey", "AAAA+/==")
	f.Add("configupgradesetkey", "x&mode=clear")
	f.Add("blob", "a=b")
	f.Fuzz(func(t *testing.T, k, v string) {
		q := url.Values{"mode": {"set"}, k: {v}}
		cmd := "upgrades?" + q.Encode()
		raw, err := rawCommand(cmd)
		if err != nil {
			return
		}
		if (Arg{ArgAdmin, cmd}).Validate() != nil || (Arg{ArgAdminRaw, raw}).Validate() != nil {
			return // refused before it reaches a host
		}
		_, rq, _ := strings.Cut(raw, "?")
		got := map[string]string{}
		for _, p := range strings.Split(rq, "&") {
			pk, pv, _ := strings.Cut(p, "=")
			got[pk] = pv
		}
		if len(got) != len(q) {
			t.Fatalf("%q decoded to %q: %d parameters, want %d", cmd, raw, len(got), len(q))
		}
		for qk, qv := range q {
			if got[qk] != qv[0] {
				t.Fatalf("%q decoded to %q: %s=%q, want %q", cmd, raw, qk, got[qk], qv[0])
			}
		}
	})
}

func TestRawCommandRefusesSplittingValues(t *testing.T) {
	for _, cmd := range []string{"upgrades?mode=set&configupgradesetkey=x%26mode%3Dclear", "info?a=%3Fb", "info?a=%23"} {
		if raw, err := rawCommand(cmd); err == nil {
			t.Errorf("%s decoded to %s", cmd, raw)
		}
	}
	if raw, err := rawCommand("upgrades?mode=set&configupgradesetkey=AA%2B%2F%3D%3D"); err != nil || raw != "upgrades?mode=set&configupgradesetkey=AA+/==" {
		t.Errorf("base64 key: %q %v", raw, err)
	}
}
