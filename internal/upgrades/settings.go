package upgrades

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// Change is one Soroban setting a proposal would change.
type Change struct {
	Setting string // configSettingID, e.g. CONFIG_SETTING_CONTRACT_MAX_SIZE_BYTES
	Field   string // path inside the setting, e.g. "contractCompute.txMaxInstructions"
	Old     string // "" when the network has no value yet
	New     string
}

// DiffSettings compares a proposal (dumpproposedsettings) with the current
// settings (sorobaninfo?format=detailed) and returns only what would change
// (SPEC.md "Soroban settings inspector"). Entries are found anywhere in
// either document as objects with a configSettingID, so the comparison does
// not depend on the wrapper around them.
func DiffSettings(current, proposed []byte) ([]Change, error) {
	cur, err := settingLeaves(current)
	if err != nil {
		return nil, fmt.Errorf("current settings: %w", err)
	}
	prop, err := settingLeaves(proposed)
	if err != nil {
		return nil, fmt.Errorf("proposal: %w", err)
	}
	var out []Change
	for id, leaves := range prop {
		for path, v := range leaves {
			old := cur[id][path]
			if old != v {
				out = append(out, Change{Setting: id, Field: path, Old: old, New: v})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Setting != out[j].Setting {
			return out[i].Setting < out[j].Setting
		}
		return out[i].Field < out[j].Field
	})
	return out, nil
}

// settingLeaves maps configSettingID to the setting's leaf values by path.
func settingLeaves(doc []byte) (map[string]map[string]string, error) {
	d := json.NewDecoder(bytes.NewReader(doc))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, errors.New("is not JSON")
	}
	out := map[string]map[string]string{}
	var walk func(any)
	walk = func(n any) {
		switch n := n.(type) {
		case map[string]any:
			if id, ok := n["configSettingID"].(string); ok {
				leaves := map[string]string{}
				for k, child := range n {
					if k != "configSettingID" {
						flatten(k, child, leaves)
					}
				}
				out[id] = leaves
				return
			}
			for _, c := range n {
				walk(c)
			}
		case []any:
			for _, c := range n {
				walk(c)
			}
		}
	}
	walk(v)
	if len(out) == 0 {
		return nil, errors.New("has no Soroban settings (no configSettingID)")
	}
	return out, nil
}

func flatten(path string, n any, into map[string]string) {
	switch n := n.(type) {
	case map[string]any:
		for k, c := range n {
			flatten(path+"."+k, c, into)
		}
	case []any:
		for i, c := range n {
			flatten(path+"["+strconv.Itoa(i)+"]", c, into)
		}
	case nil:
		into[path] = "null"
	default:
		into[path] = fmt.Sprint(n)
	}
}
