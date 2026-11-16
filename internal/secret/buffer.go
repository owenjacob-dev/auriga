package secret

import (
	"fmt"
	"log/slog"
	"unicode/utf8"
)

// Redacted is what every formatting path prints for a Buffer.
const Redacted = "[redacted]"

// Buffer holds a secret (seed, sudo password, key passphrase) in memory that
// can be wiped. It never converts itself to a string: String, GoString,
// Format, MarshalText, MarshalJSON and LogValue all yield Redacted, so an
// accidental %v, log attribute or JSON encoding cannot leak it.
//
// The zero value is an empty buffer. A Buffer must not be copied after use;
// pass *Buffer.
type Buffer struct {
	b []byte
}

// FromBytes takes ownership of b: the caller must not use b afterwards, and
// Zero clears it.
func FromBytes(b []byte) *Buffer { return &Buffer{b: b} }

// Bytes returns the secret for immediate use, such as writing it to an SSH
// stdin. The slice aliases the buffer: do not retain or modify it, and do
// not convert it to a string.
func (s *Buffer) Bytes() []byte {
	if s == nil {
		return nil
	}
	return s.b
}

// Len returns the secret's length in bytes.
func (s *Buffer) Len() int {
	if s == nil {
		return 0
	}
	return len(s.b)
}

// RuneCount returns the number of characters, for drawing mask dots.
func (s *Buffer) RuneCount() int {
	if s == nil {
		return 0
	}
	return utf8.RuneCount(s.b)
}

// AppendRune adds one typed character. When the backing array must grow,
// the old one is cleared so no stale copy stays in memory.
func (s *Buffer) AppendRune(r rune) {
	n := utf8.RuneLen(r)
	if n < 0 {
		return
	}
	if len(s.b)+n > cap(s.b) {
		grown := make([]byte, len(s.b), max(32, 2*cap(s.b)+n))
		copy(grown, s.b)
		clear(s.b[:cap(s.b)])
		s.b = grown
	}
	s.b = utf8.AppendRune(s.b, r)
}

// AppendBytes adds pasted text, with the same growth rule as AppendRune.
func (s *Buffer) AppendBytes(p []byte) {
	for len(p) > 0 {
		r, size := utf8.DecodeRune(p)
		s.AppendRune(r)
		p = p[size:]
	}
}

// Backspace removes the last character.
func (s *Buffer) Backspace() {
	if len(s.b) == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(s.b)
	clear(s.b[len(s.b)-size:])
	s.b = s.b[:len(s.b)-size]
}

// Zero overwrites the whole backing array and empties the buffer.
func (s *Buffer) Zero() {
	if s == nil {
		return
	}
	clear(s.b[:cap(s.b)])
	s.b = s.b[:0]
}

// String implements fmt.Stringer.
func (s *Buffer) String() string { return Redacted }

// GoString implements fmt.GoStringer.
func (s *Buffer) GoString() string { return Redacted }

// Format implements fmt.Formatter for every verb.
func (s *Buffer) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(Redacted)) }

// MarshalText implements encoding.TextMarshaler.
func (s *Buffer) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// MarshalJSON implements json.Marshaler.
func (s *Buffer) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// LogValue implements slog.LogValuer.
func (s *Buffer) LogValue() slog.Value { return slog.StringValue(Redacted) }
