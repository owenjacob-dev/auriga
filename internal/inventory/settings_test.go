package inventory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadSettings(dir)
	if err != nil || s != DefaultSettings() || s.PollInterval() != 5*time.Second {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	write := func(body string) {
		if err := os.WriteFile(filepath.Join(dir, SettingsFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("poll_interval_seconds = 10\n")
	if s, err := LoadSettings(dir); err != nil || s.PollIntervalSeconds != 10 || s.Theme != "auto" {
		t.Errorf("partial: %+v %v", s, err)
	}
	for _, bad := range []string{"poll_interval_seconds = 1\n", "poll_interval_seconds = 61\n", "theme = \"pink\"\n", "colour = 1\n"} {
		write(bad)
		if s, err := LoadSettings(dir); err == nil || s != DefaultSettings() {
			t.Errorf("%q: %+v %v", bad, s, err)
		}
	}
}
