package secret

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/owenjacob-dev/auriga/internal/strkey"
)

// FuzzRedact builds a real seed from fuzzed key bytes, hides it in fuzzed
// text, and checks that neither Redact nor the debug log handler lets it
// through.
func FuzzRedact(f *testing.F) {
	f.Add(make([]byte, 32), "panic: NODE_SEED=", " alias")
	f.Add(bytes.Repeat([]byte{0xff}, 32), "S", "S")
	f.Add([]byte("0123456789abcdef0123456789abcdef"), "\x00\n\"", "é")
	f.Fuzz(func(t *testing.T, key []byte, before, after string) {
		if len(key) != 32 {
			return
		}
		seed, err := strkey.Encode(strkey.VersionSeed, key)
		if err != nil {
			t.Fatal(err)
		}
		text := before + seed + after
		if out := Redact(text); strings.Contains(out, seed) {
			t.Fatalf("Redact left the seed in %q", out)
		}
		var buf bytes.Buffer
		log := slog.New(NewLogHandler(&buf, slog.LevelDebug))
		log.Info(text, "detail", text, slog.Group("g", "v", text))
		if strings.Contains(buf.String(), seed) {
			t.Fatalf("log handler wrote the seed: %s", buf.String())
		}
	})
}
