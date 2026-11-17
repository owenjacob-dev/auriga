package theme

import (
	"regexp"
	"strings"

	"charm.land/huh/v2"
)

// Form returns the theme for huh forms. With noColor the focused button is
// shown in reverse video, since StripColour removes the colours that set it
// apart.
func Form(noColor bool) huh.Theme {
	if !noColor {
		return huh.ThemeFunc(huh.ThemeBase)
	}
	return huh.ThemeFunc(func(dark bool) *huh.Styles {
		s := huh.ThemeBase(dark)
		s.Focused.FocusedButton = s.Focused.FocusedButton.Reverse(true)
		s.Blurred.FocusedButton = s.Blurred.FocusedButton.Reverse(true)
		return s
	})
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;:]*)m`)

// StripColour removes every colour from the SGR sequences in s and keeps
// the other attributes (bold, faint, reverse, underline, reset). Components
// Auriga does not style itself, such as huh's text inputs and cursor, set
// colours of their own; with NO_COLOR the whole frame goes through this.
func StripColour(s string) string {
	if !strings.Contains(s, "\x1b[") {
		return s
	}
	return sgr.ReplaceAllStringFunc(s, func(seq string) string {
		body := seq[2 : len(seq)-1]
		if body == "" {
			return seq // reset
		}
		params := strings.Split(body, ";")
		kept := params[:0:0]
		for i := 0; i < len(params); i++ {
			p := params[i]
			base, _, sub := strings.Cut(p, ":")
			switch base {
			case "38", "48", "58": // extended colour
				if !sub && i+1 < len(params) {
					switch params[i+1] {
					case "5":
						i += 2
					case "2":
						i += 4
					}
				}
				continue
			case "39", "49", "59":
				continue
			}
			if isColour(base) {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == 0 {
			return ""
		}
		return "\x1b[" + strings.Join(kept, ";") + "m"
	})
}

func isColour(p string) bool {
	if len(p) != 2 && len(p) != 3 {
		return false
	}
	switch {
	case len(p) == 2 && (p[0] == '3' || p[0] == '4' || p[0] == '9') && p[1] >= '0' && p[1] <= '7':
		return true
	case len(p) == 3 && p[0] == '1' && p[1] == '0' && p[2] >= '0' && p[2] <= '7':
		return true
	}
	return false
}
