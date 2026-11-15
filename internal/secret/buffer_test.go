package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestBufferNeverFormats(t *testing.T) {
	b := FromBytes([]byte(fakeSeed))
	outs := []string{
		fmt.Sprint(b), fmt.Sprintf("%v %+v %#v %s %q %x %X %d", b, b, b, b, b, b, b, b),
		fmt.Sprintf("%v", struct{ S *Buffer }{b}),
	}
	j, _ := json.Marshal(map[string]any{"s": b})
	outs = append(outs, string(j))
	for _, o := range outs {
		if strings.Contains(o, fakeSeed) || strings.Contains(o, fmt.Sprintf("%x", fakeSeed)) {
			t.Errorf("leaked: %q", o)
		}
	}
}

func TestBufferEditing(t *testing.T) {
	var b Buffer
	for _, r := range "pässwörd" {
		b.AppendRune(r)
	}
	if b.RuneCount() != 8 {
		t.Fatalf("RuneCount = %d", b.RuneCount())
	}
	b.Backspace()
	b.Backspace()
	if string(b.Bytes()) != "pässwö" {
		t.Fatalf("got %q", b.Bytes())
	}
	b.AppendBytes([]byte("rd"))
	backing := b.b[:cap(b.b)]
	b.Zero()
	if b.Len() != 0 || !bytes.Equal(backing, make([]byte, len(backing))) {
		t.Errorf("Zero left data: %q", backing)
	}
}

func TestBufferGrowthClearsOldArray(t *testing.T) {
	var b Buffer
	for range 32 {
		b.AppendRune('x')
	}
	old := b.b[:cap(b.b)]
	b.AppendRune('y') // forces growth past 32
	if !bytes.Equal(old, make([]byte, len(old))) {
		t.Errorf("old backing array not cleared: %q", old)
	}
	if strings.Count(string(b.Bytes()), "x") != 32 {
		t.Error("content lost on growth")
	}
}

func TestLogHandlerRedacts(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(NewLogHandler(&out, slog.LevelDebug))
	buf := FromBytes([]byte(fakeSeed))
	log.Info("read "+fakeSeed,
		"seed", "plain-secret-value",
		"pass_secret", "another-secret",
		"line", `NODE_SEED="`+fakeSeed+`"`,
		"buf", buf,
		"err", errors.New("failed with "+fakeSeed),
		slog.Group("g", "nested", fakeSeed),
		"n", 3,
	)
	got := out.String()
	for _, leak := range []string{fakeSeed, "plain-secret-value", "another-secret"} {
		if strings.Contains(got, leak) {
			t.Errorf("log contains %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, `"n":3`) {
		t.Errorf("non-secret attr lost: %s", got)
	}
}
