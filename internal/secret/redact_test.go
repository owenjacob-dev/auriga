package secret

import (
	"strings"
	"testing"
)

// fakeSeed is a syntactically valid but meaningless seed used only in tests.
const fakeSeed = "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestRedact(t *testing.T) {
	if len(fakeSeed) != 56 {
		t.Fatalf("fake seed has %d characters, want 56", len(fakeSeed))
	}
	tests := []struct {
		name, in, want string
	}{
		{"bare seed", fakeSeed, Placeholder},
		{"embedded", `NODE_SEED="` + fakeSeed + ` acme-a"`, `NODE_SEED="` + Placeholder + ` acme-a"`},
		{"two seeds", fakeSeed + " " + fakeSeed, Placeholder + " " + Placeholder},
		{"public key untouched", "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"too short", "SAAAA", "SAAAA"},
		{"plain text", "nothing to see", "nothing to see"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Redact(tt.in)
			if got != tt.want {
				t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, fakeSeed) {
				t.Errorf("output still contains the seed")
			}
		})
	}
}
