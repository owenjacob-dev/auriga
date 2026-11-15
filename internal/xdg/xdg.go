// Package xdg resolves Auriga's local config and state directories and creates
// them with owner-only permissions.
//
// Defaults follow SPEC.md "Local state and files" on both Linux and macOS:
// ~/.config/auriga and ~/.local/state/auriga, overridden by XDG_CONFIG_HOME and
// XDG_STATE_HOME.
package xdg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	appName = "auriga"
	// DirMode is used for every directory Auriga creates.
	DirMode os.FileMode = 0o700
	// FileMode is used for every file Auriga creates.
	FileMode os.FileMode = 0o600
)

// Dirs holds the resolved directories.
type Dirs struct {
	Config string
	State  string
}

// Resolve returns Auriga's directories. A non-empty configOverride (the
// --config flag) replaces the config directory. getenv is os.Getenv in
// production and a stub in tests.
func Resolve(getenv func(string) string, configOverride string) (Dirs, error) {
	home := getenv("HOME")
	base := func(env, fallback string) (string, error) {
		if v := getenv(env); v != "" {
			if !filepath.IsAbs(v) {
				return "", fmt.Errorf("%s must be an absolute path, got %q", env, v)
			}
			return filepath.Join(v, appName), nil
		}
		if home == "" {
			return "", errors.New("HOME is not set; set " + env + " instead")
		}
		return filepath.Join(home, fallback, appName), nil
	}
	var d Dirs
	var err error
	if configOverride != "" {
		d.Config, err = filepath.Abs(configOverride)
	} else {
		d.Config, err = base("XDG_CONFIG_HOME", ".config")
	}
	if err != nil {
		return Dirs{}, err
	}
	if d.State, err = base("XDG_STATE_HOME", filepath.Join(".local", "state")); err != nil {
		return Dirs{}, err
	}
	return d, nil
}

// EnsureDir creates dir (and parents) with DirMode and tightens the mode of an
// existing dir that is more permissive.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&^DirMode != 0 {
		return os.Chmod(dir, DirMode)
	}
	return nil
}

// WriteFile writes data to name atomically (temp file + rename) with FileMode,
// creating the parent directory with DirMode if needed.
func WriteFile(name string, data []byte) error {
	dir := filepath.Dir(name)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(name)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck // best effort; gone after a successful rename
	if err := f.Chmod(FileMode); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
