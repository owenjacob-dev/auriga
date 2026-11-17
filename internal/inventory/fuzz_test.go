package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoad reads arbitrary inventory files. Load must not panic, and
// anything it accepts must save, load and save again to the same bytes.
func FuzzLoad(f *testing.F) {
	f.Add([]byte("version = 1\n"))
	f.Add([]byte("version = 1\n[[orgs]]\nname = \"acme\"\nhome_domain = \"acme.org\"\nnetwork = \"pubnet\"\n" +
		"[[hosts]]\nalias = \"acme-a\"\norg = \"acme\"\nhttp_port = 11626\n"))
	f.Add([]byte("version = 2\n"))
	f.Add([]byte("version = 1\n[[hosts]]\nalias = \"a\"\n[[hosts]]\nalias = \"a\"\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), b, 0o600); err != nil {
			t.Fatal(err)
		}
		inv, err := Load(dir)
		if err != nil {
			return
		}
		out := t.TempDir()
		if err := inv.Save(out); err != nil {
			return // Save validates too; refusing is fine
		}
		again, err := Load(out)
		if err != nil {
			t.Fatalf("saved inventory does not load: %v\n%s", err, b)
		}
		out2 := t.TempDir()
		if err := again.Save(out2); err != nil {
			t.Fatalf("reloaded inventory does not save: %v", err)
		}
		first, _ := os.ReadFile(Path(out))
		second, _ := os.ReadFile(Path(out2))
		if string(first) != string(second) {
			t.Fatalf("round trip changed\n%s\nto\n%s", first, second)
		}
	})
}
