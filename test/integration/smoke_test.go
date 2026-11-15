//go:build integration

package integration

import (
	"os/exec"
	"strings"
	"testing"
)

// TestComposeStackRunning checks that `make itest` brought the stack up. Real
// scenarios arrive with the SSH layer (M1) and the admin endpoint (M2).
func TestComposeStackRunning(t *testing.T) {
	out, err := exec.Command("docker", "compose", "-f", "compose.yaml", "ps", "--status", "running", "--services").CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose ps: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "host-a") {
		t.Fatalf("host-a is not running; services: %q", out)
	}
}
