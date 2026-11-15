package sshx

import (
	"fmt"
	"os"
	"strings"

	sshconfig "github.com/kevinburke/ssh_config"
)

// resolveLibrary resolves alias with kevinburke/ssh_config when OpenSSH is
// not installed. It follows Include but ignores Match blocks.
func (r *Resolver) resolveLibrary(alias string) (HostConfig, error) {
	f, err := os.Open(r.env.configPath())
	var cfg *sshconfig.Config
	switch {
	case err == nil:
		defer f.Close() //nolint:errcheck
		cfg, err = sshconfig.Decode(f)
		if err != nil {
			return HostConfig{}, fmt.Errorf("parse %s: %w", r.env.configPath(), err)
		}
	case os.IsNotExist(err):
		cfg = &sshconfig.Config{}
	default:
		return HostConfig{}, err
	}

	get := func(key string) string {
		v, _ := cfg.Get(alias, key)
		return strings.TrimSpace(v)
	}
	getAll := func(key string) []string {
		v, _ := cfg.GetAll(alias, key)
		return v
	}

	var b strings.Builder
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s %s\n", k, v)
		}
	}
	host := get("HostName")
	if host == "" {
		host = alias
	}
	line("hostname", strings.ReplaceAll(host, "%h", alias))
	line("user", get("User"))
	line("port", get("Port"))
	for _, f := range getAll("IdentityFile") {
		line("identityfile", f)
	}
	line("identitiesonly", strings.ToLower(get("IdentitiesOnly")))
	line("proxyjump", get("ProxyJump"))
	line("proxycommand", get("ProxyCommand"))
	line("userknownhostsfile", get("UserKnownHostsFile"))
	line("globalknownhostsfile", get("GlobalKnownHostsFile"))
	line("stricthostkeychecking", get("StrictHostKeyChecking"))
	line("serveraliveinterval", get("ServerAliveInterval"))
	line("serveralivecountmax", get("ServerAliveCountMax"))
	line("connecttimeout", get("ConnectTimeout"))

	h, err := parseSSHG([]byte(b.String()), r.env)
	if err != nil {
		return HostConfig{}, fmt.Errorf("%s: %w", alias, err)
	}
	h.IdentityFiles = dedupe(h.IdentityFiles)
	h.Alias = alias
	h.Source = "ssh_config"
	return h, nil
}
