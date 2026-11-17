package upgrades

import (
	"os"
	"strings"
	"testing"
)

func TestDiffSettings(t *testing.T) {
	current, err := os.ReadFile("../../testdata/core/recorded/stellar-core-30.0.0/sorobaninfo_format_detailed.json")
	if err != nil {
		t.Fatal(err)
	}
	// A proposal in XDR-JSON form, as dumpproposedsettings prints a
	// ConfigUpgradeSet: one unchanged setting, two changed fields, one new.
	proposal := []byte(`{"updatedEntry": [
		{"configSettingID": "CONFIG_SETTING_CONTRACT_MAX_SIZE_BYTES", "contractMaxSizeBytes": 65536},
		{"configSettingID": "CONFIG_SETTING_CONTRACT_COMPUTE_V0", "contractCompute": {
			"ledgerMaxInstructions": 2500000, "txMaxInstructions": 100000000,
			"feeRatePerInstructionsIncrement": 100, "txMemoryLimit": 41943040}},
		{"configSettingID": "CONFIG_SETTING_FUTURE", "futureValue": 7}
	]}`)
	got, err := DiffSettings(current, proposal)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range got {
		lines = append(lines, c.Setting+" "+c.Field+" "+c.Old+" -> "+c.New)
	}
	want := []string{
		"CONFIG_SETTING_CONTRACT_COMPUTE_V0 contractCompute.txMaxInstructions 2500000 -> 100000000",
		"CONFIG_SETTING_CONTRACT_COMPUTE_V0 contractCompute.txMemoryLimit 2000000 -> 41943040",
		"CONFIG_SETTING_CONTRACT_MAX_SIZE_BYTES contractMaxSizeBytes 2000 -> 65536",
		"CONFIG_SETTING_FUTURE futureValue  -> 7",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	// The same settings as now: nothing would change.
	if same, err := DiffSettings(current, current); err != nil || len(same) != 0 {
		t.Errorf("self diff: %v %v", same, err)
	}
	for _, bad := range []string{"configUpgradeSet is missing or invalid", `{"updatedEntry": []}`} {
		if _, err := DiffSettings(current, []byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
