package sshx

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var testEnv = Env{Home: "/home/op", User: "op"}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSSHGFull(t *testing.T) {
	for _, fixture := range []string{"ssh-G-openssh10.0-full.txt", "ssh-G-openssh9.6-full.txt"} {
		t.Run(fixture, func(t *testing.T) { testParseSSHGFull(t, fixture) })
	}
}

func testParseSSHGFull(t *testing.T, fixture string) {
	h, err := parseSSHG(readFixture(t, fixture), testEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := HostConfig{
		HostName:              "core-a.acme.example",
		User:                  "ops",
		Port:                  2222,
		IdentityFiles:         []string{"/home/op/.ssh/acme_ed25519"},
		IdentitiesOnly:        true,
		ProxyJump:             "ops@bastion.acme.example:2200",
		UserKnownHostsFiles:   []string{"/home/op/.ssh/known_hosts", "/home/op/.ssh/acme_known_hosts"},
		GlobalKnownHostsFiles: []string{"/etc/ssh/ssh_known_hosts", "/etc/ssh/ssh_known_hosts2"},
		StrictHostKeyChecking: "yes",
		ServerAliveInterval:   10 * time.Second,
		ServerAliveCountMax:   5,
		ConnectTimeout:        7 * time.Second,
	}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("got  %+v\nwant %+v", h, want)
	}
}

func TestParseSSHGDefaults(t *testing.T) {
	h, err := parseSSHG(readFixture(t, "ssh-G-openssh10.0-defaults.txt"), testEnv)
	if err != nil {
		t.Fatal(err)
	}
	if h.HostName != "plainhost" || h.Port != 22 || h.User != "op" {
		t.Errorf("basic fields: %+v", h)
	}
	if h.ServerAliveInterval != DefaultServerAliveInterval || h.ServerAliveCountMax != 3 {
		t.Errorf("keepalive defaults: %v %d", h.ServerAliveInterval, h.ServerAliveCountMax)
	}
	if h.ConnectTimeout != DefaultConnectTimeout {
		t.Errorf("connect timeout: %v", h.ConnectTimeout)
	}
	if h.StrictHostKeyChecking != "ask" || h.ProxyJump != "" || h.ProxyCommand != "" {
		t.Errorf("strict/proxy: %+v", h)
	}
	if !slices.Contains(h.IdentityFiles, "/home/op/.ssh/id_ed25519") {
		t.Errorf("identity files not expanded: %v", h.IdentityFiles)
	}
}

func TestNormalizeStrict(t *testing.T) {
	for in, want := range map[string]string{
		"true": "yes", "yes": "yes", "false": "no", "no": "no", "off": "no",
		"ask": "ask", "accept-new": "accept-new", "": "ask", "weird": "ask",
	} {
		if got := normalizeStrict(in); got != want {
			t.Errorf("normalizeStrict(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveUsesSSHG(t *testing.T) {
	var gotArgs []string
	env := testEnv
	env.SSHPath = "/usr/bin/ssh"
	env.ConfigPath = "/tmp/cfg"
	env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotArgs = append([]string{name}, args...)
		return []byte("hostname 10.0.0.5\nport 22\nuser ops\n"), nil
	}
	h, err := NewResolver(env).Resolve(context.Background(), "acme-a")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/usr/bin/ssh", "-G", "-F", "/tmp/cfg", "--", "acme-a"}; !slices.Equal(gotArgs, want) {
		t.Errorf("argv = %v, want %v", gotArgs, want)
	}
	if h.Alias != "acme-a" || h.Source != "ssh -G" || h.Address() != "10.0.0.5:22" {
		t.Errorf("resolved %+v", h)
	}
}

func TestResolveRejectsProxyCommand(t *testing.T) {
	env := testEnv
	env.SSHPath = "/usr/bin/ssh"
	env.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("hostname h\nproxycommand nc %h %p\n"), nil
	}
	_, err := NewResolver(env).Resolve(context.Background(), "legacy")
	if err == nil || !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "ProxyCommand") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveRejectsBadAliases(t *testing.T) {
	r := NewResolver(Env{SSHPath: "-"})
	for _, a := range []string{"", "-oProxyCommand=x", "a b", "a*", "a;b\x00", `a"b`} {
		if _, err := r.Resolve(context.Background(), a); err == nil {
			t.Errorf("alias %q accepted", a)
		}
	}
}

func TestLibraryFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	// The library resolves relative Include paths against the passwd home
	// directory, which a test cannot redirect, so this Include is absolute.
	writeFile(t, filepath.Join(sshDir, "config"), `
Include `+filepath.Join(sshDir, "conf.d", "*.conf")+`
Host acme-b
  HostName 10.1.0.2
  User ops
  Port 2201
  IdentityFile ~/.ssh/acme
  StrictHostKeyChecking yes
Match host *.internal
  User wrong
`)
	writeFile(t, filepath.Join(sshDir, "conf.d", "10-c.conf"), "Host acme-c\n  HostName 10.1.0.3\n  ProxyJump acme-b\n")
	env := Env{Home: home, User: "me", SSHPath: "-"}
	r := NewResolver(env)
	if r.Warning == "" {
		t.Error("no Match warning in fallback mode")
	}
	h, err := r.Resolve(context.Background(), "acme-b")
	if err != nil {
		t.Fatal(err)
	}
	if h.Address() != "10.1.0.2:2201" || h.User != "ops" || h.StrictHostKeyChecking != "yes" || h.Source != "ssh_config" {
		t.Errorf("acme-b: %+v", h)
	}
	if !slices.Equal(h.IdentityFiles, []string{filepath.Join(home, ".ssh", "acme")}) {
		t.Errorf("identity files %v", h.IdentityFiles)
	}
	c, err := r.Resolve(context.Background(), "acme-c")
	if err != nil {
		t.Fatal(err)
	}
	if c.HostName != "10.1.0.3" || c.ProxyJump != "acme-b" || c.User != "me" {
		t.Errorf("acme-c via Include: %+v", c)
	}
	d, err := r.Resolve(context.Background(), "unlisted")
	if err != nil {
		t.Fatal(err)
	}
	if d.HostName != "unlisted" || d.Port != 22 || len(d.IdentityFiles) == 0 || strings.HasSuffix(d.IdentityFiles[0], "/identity") {
		t.Errorf("defaults: %+v", d)
	}
}

func TestListAliases(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	writeFile(t, filepath.Join(sshDir, "config"), `
# comment
Host acme-a acme-b   # trailing comment
  HostName x
Host=acme-c
Host *.example.com !skip ?ingle
Host "quoted-host"
Include conf.d/*.conf ~/.ssh/extra missing-file
Host acme-a
Match host foo
`)
	writeFile(t, filepath.Join(sshDir, "conf.d", "20-b.conf"), "Host zeta\n")
	writeFile(t, filepath.Join(sshDir, "conf.d", "10-a.conf"), "Host alpha\nInclude ~/.ssh/nested\n")
	writeFile(t, filepath.Join(sshDir, "nested"), "Host nested-one\n")
	writeFile(t, filepath.Join(sshDir, "extra"), "HOST extra-one\n")
	got, err := ListAliases(Env{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acme-a", "acme-b", "acme-c", "quoted-host", "alpha", "nested-one", "zeta", "extra-one"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestListAliasesMissingConfig(t *testing.T) {
	got, err := ListAliases(Env{Home: t.TempDir()})
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestListAliasesIncludeLoop(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".ssh", "config"), "Host a\nInclude config\n")
	if _, err := ListAliases(Env{Home: home}); err == nil {
		t.Error("include loop not detected")
	}
}

func TestParseProxyJump(t *testing.T) {
	hops, err := ParseProxyJump("ops@bastion:2200,inner,[fe80::1]:22,ssh://u@h")
	if err != nil {
		t.Fatal(err)
	}
	want := []JumpHop{
		{User: "ops", Host: "bastion", Port: 2200},
		{Host: "inner"},
		{Host: "fe80::1", Port: 22},
		{User: "u", Host: "h"},
	}
	if !reflect.DeepEqual(hops, want) {
		t.Errorf("got %+v", hops)
	}
	for _, bad := range []string{"h:x", "[fe80::1", "-oX=y", "a b"} {
		if _, err := ParseProxyJump(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if hops, _ := ParseProxyJump("none"); hops != nil {
		t.Error("none should mean no hops")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultEnvUsesPasswdHome(t *testing.T) {
	t.Setenv("HOME", "/somewhere/else")
	env := DefaultEnv()
	if env.Home == "/somewhere/else" || env.Home == "" || env.User == "" {
		t.Errorf("DefaultEnv = %+v; want the passwd home, not $HOME", env)
	}
}
