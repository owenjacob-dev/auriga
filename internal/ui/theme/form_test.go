package theme

import "testing"

func TestStripColour(t *testing.T) {
	cases := map[string]string{
		"plain":                            "plain",
		"\x1b[37m> \x1b[m":                 "> \x1b[m",
		"\x1b[1;31mbold red\x1b[0m":        "\x1b[1mbold red\x1b[0m",
		"\x1b[38;5;99mx":                   "x",
		"\x1b[38;2;1;2;3;7mx":              "\x1b[7mx",
		"\x1b[48:2::1:2:3;4mx":             "\x1b[4mx",
		"\x1b[30;47;7mbutton":              "\x1b[7mbutton",
		"\x1b[92;104;39;49mx":              "x",
		"\x1b[2;3mfaint italic":            "\x1b[2;3mfaint italic",
		"\x1b[1m\x1b[38;5;1m\x1b[22mmixed": "\x1b[1m\x1b[22mmixed",
	}
	for in, want := range cases {
		if got := StripColour(in); got != want {
			t.Errorf("StripColour(%q) = %q, want %q", in, got, want)
		}
	}
}
