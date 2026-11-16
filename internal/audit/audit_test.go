package audit

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const seed = "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4"

func TestAppendReadFilter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	l := Open(dir)
	t0 := time.Date(2026, 11, 2, 13, 10, 4, 0, time.UTC)
	entries := []Entry{
		{Time: t0, Host: "acme-a", Action: "deploy.stage", ConfigSHA256: "9f2c", Result: OK},
		{Time: t0.Add(time.Second), Host: "acme-a", Action: "deploy.install", Backup: "/etc/stellar/x.bak", Result: OK},
		{Time: t0.Add(2 * time.Second), Host: "acme-b", Action: "rollback.restore", Result: Failed, Error: "stage: bad seed " + seed},
	}
	for _, e := range entries {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(l.Path())
	if strings.Contains(string(raw), seed) {
		t.Fatal("seed written to the audit log")
	}
	if !strings.Contains(string(raw), `{"ts":"2026-11-02T13:10:05Z","host":"acme-a","action":"deploy.install","backup":"/etc/stellar/x.bak","result":"ok"}`) {
		t.Errorf("line format:\n%s", raw)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, l.Path(): 0o600} {
		if fi, _ := os.Stat(p); fi.Mode().Perm() != want {
			t.Errorf("%s mode %v", p, fi.Mode())
		}
	}
	os.WriteFile(l.Path(), append(raw, "not json\n\n"...), 0o600)
	got, bad, err := l.Read()
	if err != nil || bad != 1 || len(got) != 3 || got[0].Host != "acme-b" || got[2].Action != "deploy.stage" {
		t.Fatalf("read %+v %d %v", got, bad, err)
	}
	if f := Filter(got, "acme-a", "deploy."); len(f) != 2 {
		t.Errorf("filter host+action: %d", len(f))
	}
	if f := Filter(got, "", "rollback"); len(f) != 1 {
		t.Errorf("filter action: %d", len(f))
	}
	if err := l.Append(Entry{Host: "x"}); err == nil {
		t.Error("incomplete entry accepted")
	}
}

func TestEmptyAndConcurrent(t *testing.T) {
	l := Open(t.TempDir())
	if got, _, err := l.Read(); err != nil || got != nil {
		t.Fatalf("missing log: %v %v", got, err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { l.Append(Entry{Host: "h", Action: "deploy.restart", Result: OK}) })
	}
	wg.Wait()
	if got, bad, _ := l.Read(); len(got) != 50 || bad != 0 {
		t.Errorf("%d entries, %d bad", len(got), bad)
	}
}
