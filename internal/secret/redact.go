// Package secret holds the helpers that keep Stellar secret seeds and other
// credentials out of logs, crash reports and the screen.
package secret

import "regexp"

// seedPattern matches a Stellar secret seed in StrKey form: the version byte
// 'S' followed by 55 base32 characters (56 characters in total).
var seedPattern = regexp.MustCompile(`S[A-Z2-7]{55}`)

// Placeholder replaces every redacted value.
const Placeholder = "S…[redacted]"

// Redact replaces anything shaped like a Stellar secret seed with Placeholder.
// It is a defence-in-depth filter for free text such as panic messages and log
// lines; code must still never put a secret into such text deliberately.
func Redact(s string) string {
	return seedPattern.ReplaceAllString(s, Placeholder)
}
