package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/secret"
)

// SecretInput is a masked field that keeps what is typed in a secret.Buffer
// (CLAUDE.md hard rule 1). Keystrokes and pastes arrive from Bubble Tea as
// short-lived strings; they are copied into the buffer at once and never
// stored elsewhere.
type SecretInput struct {
	buf *secret.Buffer
}

// NewSecretInput returns an empty field.
func NewSecretInput() *SecretInput { return &SecretInput{buf: &secret.Buffer{}} }

// Update handles typing, paste and backspace. It reports whether msg was
// consumed.
func (s *SecretInput) Update(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		s.buf.AppendBytes([]byte(strings.TrimRight(msg.Content, "\r\n")))
		return true
	case tea.KeyPressMsg:
		switch {
		case msg.Code == tea.KeyBackspace:
			s.buf.Backspace()
			return true
		case msg.Text != "" && msg.Mod&^tea.ModShift == 0:
			for _, r := range msg.Text {
				s.buf.AppendRune(r)
			}
			return true
		}
	}
	return false
}

// Take returns the buffer and leaves the field empty with a fresh buffer.
// The caller owns the returned buffer.
func (s *SecretInput) Take() *secret.Buffer {
	b := s.buf
	s.buf = &secret.Buffer{}
	return b
}

// Clear zeroes what was typed.
func (s *SecretInput) Clear() { s.buf.Zero() }

// Len is the number of characters typed.
func (s *SecretInput) Len() int { return s.buf.RuneCount() }

// View shows one dot per character, capped so the length of a long secret is
// not revealed exactly.
func (s *SecretInput) View(width int) string {
	n := min(s.buf.RuneCount(), 24, max(0, width-1))
	return strings.Repeat("•", n) + "▏"
}
