package corehttp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecode feeds arbitrary bodies to every admin response decoder and to
// the error reader. None may panic; bodies come from a host Auriga does not
// control.
func FuzzDecode(f *testing.F) {
	recorded, _ := filepath.Glob("../../testdata/core/recorded/*/*.json")
	for _, p := range recorded {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // test fixtures
			f.Add(b)
		}
	}
	for _, s := range []string{`{"exception":"x"}`, "plain error", `{"info":{"quorum":{"qset":{"value":{"v":[{"v":[]}]}}}}}`,
		`{"qset":{"agree":[1,2]},"transitive":{"critical":[[1]]}}`, `{"fee":{"data":1,"nullopt":false}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var info InfoResponse
		if json.Unmarshal(b, &info) == nil {
			_ = info.Info.Armed()
		}
		var q QuorumResponse
		if json.Unmarshal(b, &q) == nil && q.QSet != nil {
			_ = q.QSet.Agree.N()
			_, _ = json.Marshal(q)
		}
		var p PeersResponse
		_ = json.Unmarshal(b, &p)
		var u UpgradesResponse
		if json.Unmarshal(b, &u) == nil {
			_ = u.Scheduled()
		}
		for _, wantJSON := range []bool{true, false} {
			_ = coreError(CmdUpgradesClear(), b, wantJSON)
		}
	})
}
