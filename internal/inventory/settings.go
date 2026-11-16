package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// SettingsFileName is the settings file inside the config directory.
const SettingsFileName = "settings.toml"

// Settings are the operator's preferences (SPEC.md "Local state and files").
type Settings struct {
	// PollIntervalSeconds is how often info is polled: 2 to 60, default 5.
	PollIntervalSeconds int `toml:"poll_interval_seconds"`
	// Theme is "auto" (follow the terminal), "dark" or "light".
	Theme string `toml:"theme"`
}

// DefaultSettings returns the defaults.
func DefaultSettings() Settings { return Settings{PollIntervalSeconds: 5, Theme: "auto"} }

// PollInterval returns the interval as a duration.
func (s Settings) PollInterval() time.Duration {
	return time.Duration(s.PollIntervalSeconds) * time.Second
}

// Validate checks ranges.
func (s Settings) Validate() error {
	if s.PollIntervalSeconds < 2 || s.PollIntervalSeconds > 60 {
		return fmt.Errorf("poll_interval_seconds must be 2 to 60, got %d", s.PollIntervalSeconds)
	}
	switch s.Theme {
	case "auto", "dark", "light":
	default:
		return fmt.Errorf("theme must be auto, dark or light, got %q", s.Theme)
	}
	return nil
}

// LoadSettings reads settings.toml from configDir. A missing file gives the
// defaults; keys left out keep their defaults.
func LoadSettings(configDir string) (Settings, error) {
	s := DefaultSettings()
	path := filepath.Join(configDir, SettingsFileName)
	b, err := os.ReadFile(path) //nolint:gosec // Auriga's own file
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields().Decode(&s); err != nil {
		return DefaultSettings(), fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return DefaultSettings(), fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}
