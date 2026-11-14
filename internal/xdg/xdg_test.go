package xdg

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		override string
		want     Dirs
		wantErr  bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"HOME": "/home/op"},
			want: Dirs{Config: "/home/op/.config/auriga", State: "/home/op/.local/state/auriga"},
		},
		{
			name: "xdg overrides",
			env:  map[string]string{"HOME": "/home/op", "XDG_CONFIG_HOME": "/c", "XDG_STATE_HOME": "/s"},
			want: Dirs{Config: "/c/auriga", State: "/s/auriga"},
		},
		{
			name:     "flag overrides config only",
			env:      map[string]string{"HOME": "/home/op", "XDG_CONFIG_HOME": "/c"},
			override: "/etc/auriga-test",
			want:     Dirs{Config: "/etc/auriga-test", State: "/home/op/.local/state/auriga"},
		},
		{name: "relative xdg rejected", env: map[string]string{"HOME": "/h", "XDG_STATE_HOME": "rel"}, wantErr: true},
		{name: "no home", env: map[string]string{}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(env(tt.env), tt.override)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWriteFileModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	name := filepath.Join(dir, "f.toml")
	if err := WriteFile(name, []byte("x")); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, DirMode)
	assertMode(t, name, FileMode)
	// Overwrite keeps the mode and replaces the content.
	if err := WriteFile(name, []byte("y")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(name)
	if string(b) != "y" {
		t.Errorf("content = %q", b)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestEnsureDirTightens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	assertMode(t, dir, DirMode)
}

func assertMode(t *testing.T, p string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", p, got, want)
	}
}
