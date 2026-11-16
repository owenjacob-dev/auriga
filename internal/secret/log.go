package secret

import (
	"io"
	"log/slog"
	"strings"
)

// secretKeys are attribute keys whose values are always masked, whatever
// they hold. Code tags a field as secret by using one of these keys or the
// suffix "_secret".
var secretKeys = map[string]bool{
	"seed": true, "node_seed": true, "password": true, "passphrase": true,
	"sudo_password": true, "secret": true,
}

func isSecretKey(k string) bool {
	k = strings.ToLower(k)
	return secretKeys[k] || strings.HasSuffix(k, "_secret")
}

// NewLogHandler returns a JSON slog handler that masks secret-tagged
// attributes and redacts anything shaped like a secret seed in the message
// and in every string value before it is written (SPEC.md "Logging").
func NewLogHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if isSecretKey(a.Key) {
				return slog.String(a.Key, Redacted)
			}
			v := a.Value.Resolve()
			switch v.Kind() {
			case slog.KindString:
				return slog.String(a.Key, Redact(v.String()))
			case slog.KindAny:
				// Errors and other values are rendered as text, then redacted.
				if err, ok := v.Any().(error); ok {
					return slog.String(a.Key, Redact(err.Error()))
				}
				return slog.String(a.Key, Redact(v.String()))
			}
			return slog.Attr{Key: a.Key, Value: v}
		},
	})
}

// Discard is a logger that writes nothing, used when --debug is off.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
