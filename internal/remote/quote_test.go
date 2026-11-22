package remote

import (
	"bytes"
	"math/rand/v2"
	"os/exec"
	"strings"
	"testing"
)

var quoteSeeds = []string{"", "a", "it's", `"$(x)"`, "a\nb", "'''", "\\", "a b\tc", "$HOME `id` ;|&<>*?[]#~"}

// FuzzQuote checks Quote against how a POSIX shell reads the result
// (shellWord). It runs no shell: starting one per input made the fuzzer
// slow to stop when -fuzztime ran out on a loaded CI runner, which Go
// reports as "context deadline exceeded". TestQuoteInRealShells runs real
// shells over a fixed set of inputs instead.
func FuzzQuote(f *testing.F) {
	for _, s := range quoteSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q := Quote(s)
		got, ok := shellWord(q)
		if !ok {
			t.Fatalf("Quote(%q) = %s is not exactly one shell word", s, q)
		}
		if got != s {
			t.Fatalf("Quote(%q) = %s reads back as %q", s, q, got)
		}
	})
}

// shellWord reads q as a POSIX shell (XCU 2.2) reads a word made only of
// single-quoted spans and backslash-escaped characters, the two forms Quote
// uses. Anything else unquoted (blanks, $, `, ", operators) or an unclosed
// quote means q is not one literal word, and ok is false.
func shellWord(q string) (word string, ok bool) {
	if q == "" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(q); {
		switch q[i] {
		case '\'':
			end := strings.IndexByte(q[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(q[i+1 : i+1+end])
			i += end + 2
		case '\\':
			if i+1 >= len(q) || q[i+1] == '\n' {
				return "", false // a trailing backslash, or a line continuation
			}
			b.WriteByte(q[i+1])
			i += 2
		default:
			return "", false
		}
	}
	return b.String(), true
}

func TestShellWord(t *testing.T) {
	for q, want := range map[string]string{`'a'`: "a", `''`: "", `'a'\''b'`: "a'b", `\'`: "'", `'x y'`: "x y"} {
		if got, ok := shellWord(q); !ok || got != want {
			t.Errorf("shellWord(%s) = %q, %v; want %q", q, got, ok, want)
		}
	}
	for _, q := range []string{"", "a", "'a", "'a' 'b'", `'a'$x`, `"a"`, `\`, "\\\n"} {
		if got, ok := shellWord(q); ok {
			t.Errorf("shellWord(%q) accepted as %q", q, got)
		}
	}
}

// TestQuoteInRealShells round-trips a fixed set of inputs through each
// shell found: every byte value but NUL, the fuzz seeds, and 500
// pseudo-random strings biased towards shell metacharacters. Each shell runs
// once and prints the words NUL-separated (argv cannot carry NUL).
func TestQuoteInRealShells(t *testing.T) {
	inputs := append([]string(nil), quoteSeeds...)
	var all strings.Builder
	for c := 1; c < 256; c++ {
		inputs = append(inputs, string([]byte{byte(c)}))
		all.WriteByte(byte(c))
	}
	inputs = append(inputs, all.String())
	r := rand.New(rand.NewPCG(1, 2))
	const alphabet = "'\"\\$`!*?[]{}()<>|&;#~= \t\nab\x01\x7f\xc3\xa9"
	for range 500 {
		var b strings.Builder
		for range r.IntN(24) {
			if r.IntN(4) == 0 {
				b.WriteByte(byte(1 + r.IntN(255)))
			} else {
				b.WriteByte(alphabet[r.IntN(len(alphabet))])
			}
		}
		inputs = append(inputs, b.String())
	}
	var script strings.Builder
	for _, s := range inputs {
		script.WriteString(`printf '%s\0' ` + Quote(s) + "\n")
	}
	ran := 0
	for _, sh := range []string{"sh", "dash", "bash"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		ran++
		out, err := exec.Command(path, "-c", script.String()).Output()
		if err != nil {
			t.Fatalf("%s: %v", sh, err)
		}
		got := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
		if len(got) != len(inputs) {
			t.Fatalf("%s printed %d words for %d inputs", sh, len(got), len(inputs))
		}
		for i, s := range inputs {
			if string(got[i]) != s {
				t.Errorf("%s: Quote(%q) round-tripped to %q", sh, s, got[i])
			}
		}
	}
	if ran == 0 {
		t.Skip("no POSIX shell found")
	}
}
