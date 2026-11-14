package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "auriga dev (") {
		t.Errorf("version output = %q", out.String())
	}
}

func TestBadFlags(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"extra"}} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestHelpFlagIsNotAnError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Errorf("exit %d", code)
	}
	if !strings.Contains(errOut.String(), "--config") && !strings.Contains(errOut.String(), "-config") {
		t.Errorf("usage missing --config: %q", errOut.String())
	}
}
