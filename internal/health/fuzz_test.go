package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
)

// FuzzEvaluate decodes an arbitrary info body and evaluates it against a
// healthy host, as the dashboard does. Nothing may panic, and every row
// must explain each cell that is not OK.
func FuzzEvaluate(f *testing.F) {
	recorded, _ := filepath.Glob("../../testdata/core/recorded/*/info*.json")
	for _, p := range recorded {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // test fixtures
			f.Add(b)
		}
	}
	f.Add([]byte(`{"info":{"state":"Synced!","ledger":{"num":18446744073709551615,"age":-5},"status":["Armed with network upgrades: x"]}}`))
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, b []byte) {
		var r corehttp.InfoResponse
		if json.Unmarshal(b, &r) != nil {
			return
		}
		good := corehttp.Info{State: "Synced!", Build: "stellar-core 30.0.0", ProtocolVersion: 30}
		rows := Evaluate([]Sample{
			{Alias: "a", Org: "acme", Network: "testnet", Info: &r.Info, At: now.Add(-time.Second)},
			{Alias: "b", Org: "acme", Network: "testnet", Info: &good, At: now},
		}, now, 5*time.Second)
		for alias, row := range rows {
			if row.Overall >= Warn && len(row.Problems) == 0 {
				t.Fatalf("%s is %v with no problem stated: %+v", alias, row.Overall, row)
			}
		}
		_ = UpgradeLabel(r.Info.Armed())
	})
}
