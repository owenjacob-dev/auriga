package quorum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/owenjacob-dev/auriga/internal/stellarcfg"
)

type coreResult struct {
	File    string          `json:"file"`
	Verdict string          `json:"verdict"`
	Message string          `json:"message"`
	QSet    json.RawMessage `json:"qset"`
}

// coreShape canonicalises stellar-core's QUORUM_SET JSON like Set.Shape.
func coreShape(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var q struct {
		T int               `json:"t"`
		V []json.RawMessage `json:"v"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, m := range q.V {
		var name string
		if json.Unmarshal(m, &name) == nil {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, coreShape(t, m))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d:[%s]", q.T, strings.Join(parts, ","))
}

func selfOf(c *stellarcfg.Config) Self {
	return Self{Name: c.NodeSeed.Alias, PublicKey: c.NodeSeed.PublicKey, HomeDomain: c.NodeHomeDomain}
}

// TestMatchesStellarCore30 compares the preview and the verdict with what
// stellar-core 30.0.0 did for each fixture.
func TestMatchesStellarCore30(t *testing.T) {
	f, err := os.Open("testdata/core30/expected.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var want coreResult
		if err := json.Unmarshal(sc.Bytes(), &want); err != nil {
			t.Fatal(err)
		}
		n++
		t.Run(want.File, func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join("testdata", "core30", want.File))
			if err != nil {
				t.Fatal(err)
			}
			res, err := stellarcfg.Parse(text)
			if err != nil {
				t.Fatal(err)
			}
			cfg, self := res.Config, selfOf(res.Config)
			issues := Check(cfg, self, CheckOptions{})
			refused := Blocking(issues, false) || len(stellarcfg.Blocking(stellarcfg.Validate(cfg, stellarcfg.ValidateOptions{}))) > 0
			if refused != (want.Verdict == "refused") {
				t.Errorf("we %s it, core %s it (%s); our issues: %+v", verdict(refused), want.Verdict, want.Message, issues)
			}
			if string(want.QSet) != "null" {
				if got, exp := Generate(cfg, self).Shape(), coreShape(t, want.QSet); got != exp {
					t.Errorf("generated set\n got %s\nwant %s", got, exp)
				}
			}
		})
	}
	if n != 14 {
		t.Fatalf("read %d fixtures, want 14", n)
	}
}

func verdict(refused bool) string {
	if refused {
		return "refuse"
	}
	return "accept"
}

func cfgWith(hds []stellarcfg.HomeDomain, vals ...stellarcfg.Validator) *stellarcfg.Config {
	return &stellarcfg.Config{HomeDomains: hds, Validators: vals}
}

var self = Self{Name: "own-a", PublicKey: "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H", HomeDomain: "own.example"}

func v(name, domain string, history bool) stellarcfg.Validator {
	val := stellarcfg.Validator{Name: name, HomeDomain: domain, PublicKey: "G" + strings.Repeat(strings.ToUpper(name[:1]), 55)}
	if history {
		val.History = "curl -sf https://h/" + name + "/{0} -o {1}"
	}
	return val
}

func has(issues []Issue, rule string, sev Severity) bool {
	for _, i := range issues {
		if i.Rule == rule && i.Severity == sev {
			return true
		}
	}
	return false
}

func TestCheckRules(t *testing.T) {
	medium := stellarcfg.HomeDomain{HomeDomain: "own.example", Quality: stellarcfg.Medium}
	high := func(d string) stellarcfg.HomeDomain {
		return stellarcfg.HomeDomain{HomeDomain: d, Quality: stellarcfg.High}
	}
	crit := stellarcfg.HomeDomain{HomeDomain: "c.example", Quality: stellarcfg.Critical}

	t.Run("no quality", func(t *testing.T) {
		is := Check(cfgWith([]stellarcfg.HomeDomain{medium}, v("x1", "nowhere.example", false)), self, CheckOptions{})
		if !has(is, "no-quality", Block) {
			t.Errorf("%+v", is)
		}
	})
	t.Run("node quality overrides domain", func(t *testing.T) {
		val := v("x1", "nowhere.example", false)
		val.Quality = stellarcfg.Low
		if has(Check(cfgWith([]stellarcfg.HomeDomain{medium}, val), self, CheckOptions{}), "no-quality", Block) {
			t.Error("node quality ignored")
		}
	})
	t.Run("high org needs three", func(t *testing.T) {
		is := Check(cfgWith([]stellarcfg.HomeDomain{medium, high("h.example")}, v("h1", "h.example", true), v("h2", "h.example", true)), self, CheckOptions{})
		if !has(is, "redundancy", Block) {
			t.Errorf("%+v", is)
		}
	})
	t.Run("own high org counts this node", func(t *testing.T) {
		cfg := cfgWith([]stellarcfg.HomeDomain{high("own.example")}, v("o2", "own.example", true), v("o3", "own.example", true))
		if has(Check(cfg, self, CheckOptions{}), "redundancy", Block) {
			t.Error("this node not counted for its own org")
		}
	})
	t.Run("high needs history", func(t *testing.T) {
		cfg := cfgWith([]stellarcfg.HomeDomain{high("own.example")}, v("o2", "own.example", true), v("o3", "own.example", false))
		if !has(Check(cfg, self, CheckOptions{}), "history-required", Block) {
			t.Error("missing HISTORY not blocked")
		}
	})
	t.Run("critical warns", func(t *testing.T) {
		cfg := cfgWith([]stellarcfg.HomeDomain{medium, crit}, v("c1", "c.example", true), v("c2", "c.example", true), v("c3", "c.example", true))
		if !has(Check(cfg, self, CheckOptions{}), "critical", Warn) {
			t.Error("CRITICAL not warned")
		}
	})
	t.Run("zero tolerance needs consent until UNSAFE_QUORUM", func(t *testing.T) {
		cfg := cfgWith([]stellarcfg.HomeDomain{medium, high("h.example")}, v("h1", "h.example", true), v("h2", "h.example", true), v("h3", "h.example", true))
		is := Check(cfg, self, CheckOptions{})
		if !has(is, "zero-tolerance", NeedsConsent) || !has(is, "few-entities", Warn) || !Blocking(is, false) || Blocking(is, true) {
			t.Errorf("%+v", is)
		}
		cfg.UnsafeQuorum = stellarcfg.Ptr(true)
		if !has(Check(cfg, self, CheckOptions{}), "zero-tolerance", Warn) {
			t.Error("UNSAFE_QUORUM=true not recognised")
		}
	})
	t.Run("self listed", func(t *testing.T) {
		val := v("other-name", "own.example", false)
		val.PublicKey = self.PublicKey
		if !has(Check(cfgWith([]stellarcfg.HomeDomain{medium}, val), self, CheckOptions{}), "self-listed", Block) {
			t.Error("own key in VALIDATORS not blocked")
		}
	})
	t.Run("removed validators", func(t *testing.T) {
		running := cfgWith([]stellarcfg.HomeDomain{medium}, v("o2", "own.example", false), v("gone", "own.example", false))
		is := Check(cfgWith([]stellarcfg.HomeDomain{medium}, v("o2", "own.example", false)), self, CheckOptions{Running: running})
		if !has(is, "removed", Warn) || !strings.Contains(fmt.Sprint(is), "gone") {
			t.Errorf("%+v", is)
		}
	})
}

func TestPreviewLines(t *testing.T) {
	cfg := cfgWith([]stellarcfg.HomeDomain{
		{HomeDomain: "own.example", Quality: stellarcfg.Medium},
		{HomeDomain: "a.example", Quality: stellarcfg.High}, {HomeDomain: "b.example", Quality: stellarcfg.High}, {HomeDomain: "c.example", Quality: stellarcfg.High},
	}, v("own-b", "own.example", false), v("own-c", "own.example", false),
		v("a1", "a.example", true), v("a2", "a.example", true), v("a3", "a.example", true),
		v("b1", "b.example", true), v("b2", "b.example", true), v("b3", "b.example", true),
		v("c1", "c.example", true), v("c2", "c.example", true), v("c3", "c.example", true))
	set := Generate(cfg, self)
	got := strings.Join(set.Lines(), "\n")
	for _, want := range []string{"3 of 4  HIGH  (tolerates 1 failure)", "  2 of 3  own.example  (tolerates 1 failure)", "own-a (this node)  own.example MEDIUM"} {
		if !strings.Contains(got, want) {
			t.Errorf("preview lacks %q:\n%s", want, got)
		}
	}
}
