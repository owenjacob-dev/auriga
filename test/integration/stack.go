//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Public test vectors (internal/strkey); the hosts' configs use these seeds.
const (
	SeedA    = "SDHOAMBNLGCE2MV5ZKIVZAQD3VCLGP53P3OBSBI6UN5L5XZI5TKHFQL4"
	AddressA = "GBRPYHIL2CI3FNQ4BXLFMNDLFJUNPU2HY3ZMFSHONUCEOASW7QC7OX2H"
	SeedB    = "SAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB6NKI"
	AddressB = "GAB2CB576PHBBPQ5ODORRZ2LYCMWPZGWGCN2KDK7DXOIMZASKUY3QZ6Q"
	// SudoPassword is op's sudo password on host-b.
	SudoPassword = "op-sudo-pw"
)

// Dir is the test/integration directory.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// Compose runs docker compose against the stack.
func Compose(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", filepath.Join(Dir(), "compose.yaml")}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// HostKeyLine returns a known_hosts line for service's ed25519 key at addr.
func HostKeyLine(t testing.TB, service, addr string) string {
	t.Helper()
	pub := strings.TrimSpace(Compose(t, "exec", "-T", service, "cat", "/etc/ssh/ssh_host_ed25519_key.pub"))
	fields := strings.Fields(pub)
	return fmt.Sprintf("%s %s %s", addr, fields[0], fields[1])
}

// Home is a temporary home with ~/.ssh/config for the stack:
// it-a, it-b (published ports), it-bastion, and it-c behind it-bastion.
type Home struct {
	Dir        string
	ConfigPath string
	KnownHosts string
	Key        string
}

// NewHome writes the ssh config. When trustAll is set, known_hosts already
// holds every host key; otherwise it is empty and prompts will be raised.
func NewHome(t testing.TB, trustAll bool) Home {
	t.Helper()
	dir := t.TempDir()
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, _ := filepath.Abs(filepath.Join(Dir(), ".work", "id_ed25519"))
	h := Home{Dir: dir, ConfigPath: filepath.Join(sshDir, "config"), KnownHosts: filepath.Join(sshDir, "known_hosts"), Key: key}
	cfg := fmt.Sprintf(`Host it-a
  HostName 127.0.0.1
  Port 2221
Host it-b
  HostName 127.0.0.1
  Port 2222
Host it-bastion
  HostName 127.0.0.1
  Port 2220
Host it-c
  HostName host-c
  ProxyJump it-bastion
Host it-*
  User op
  IdentityFile %s
  IdentitiesOnly yes
  UserKnownHostsFile %s
  GlobalKnownHostsFile /dev/null
  ServerAliveInterval 1
  ServerAliveCountMax 2
  ConnectTimeout 5
`, key, h.KnownHosts)
	if err := os.WriteFile(h.ConfigPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var kh []string
	if trustAll {
		kh = []string{
			HostKeyLine(t, "host-a", "[127.0.0.1]:2221"),
			HostKeyLine(t, "host-b", "[127.0.0.1]:2222"),
			HostKeyLine(t, "bastion", "[127.0.0.1]:2220"),
			HostKeyLine(t, "host-c", "host-c"),
		}
	}
	if err := os.WriteFile(h.KnownHosts, []byte(strings.Join(kh, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}
