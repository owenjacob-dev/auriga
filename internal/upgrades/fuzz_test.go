package upgrades

import (
	"reflect"
	"testing"
)

// FuzzParse feeds arbitrary pastes to the arming-string parser. Whatever it
// accepts must survive a round trip through the request it would send.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"upgrades?mode=set&upgradetime=2026-09-16T17:00:00Z&protocolversion=28",
		`curl "http://127.0.0.1:11626/upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&basefee=200"`,
		"mode=set&upgradetime=2026-11-23T17%3A00%3A00Z&maxtxsetsize=1000&expirationminutes=15",
		"upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&configupgradesetkey=" + validKeyForFuzz,
		"upgrades?mode=clear", "&&&", "upgrades?mode=set&basefee=99999999999",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := Parse(s)
		if err != nil {
			return
		}
		again, err := Parse(p.Query().Encode())
		if err != nil {
			t.Fatalf("Parse(%q) = %v, but its query %q does not parse: %v", s, p, p.Query().Encode(), err)
		}
		if !again.Time.Equal(p.Time) || !reflect.DeepEqual(again.Values, p.Values) || again.ConfigUpgradeSetKey != p.ConfigUpgradeSetKey {
			t.Fatalf("round trip changed %+v to %+v", p, again)
		}
	})
}

// 64 zero bytes, base64.
const validKeyForFuzz = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
