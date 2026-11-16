// Package sshx is Auriga's SSH layer: reading ~/.ssh/config, authenticating
// with agent and key files, verifying host keys, dialling through ProxyJump
// chains, and holding one long-lived connection per host (SPEC.md "SSH
// layer").
package sshx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// HostConfig is an alias resolved the way OpenSSH would resolve it.
type HostConfig struct {
	Alias                 string
	HostName              string
	User                  string
	Port                  int
	IdentityFiles         []string // expanded, in order
	IdentitiesOnly        bool
	ProxyJump             string // "" when not set
	ProxyCommand          string // "" when not set; unsupported in v1
	UserKnownHostsFiles   []string
	GlobalKnownHostsFiles []string
	StrictHostKeyChecking string // "yes", "ask", "accept-new", "no", …
	ServerAliveInterval   time.Duration
	ServerAliveCountMax   int
	ConnectTimeout        time.Duration
	// Source says how the alias was resolved: "ssh -G" or "ssh_config".
	Source string
}

// Address returns host:port for dialling.
func (h HostConfig) Address() string {
	return joinHostPort(h.HostName, h.Port)
}

func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// Defaults applied when the config leaves a value unset (SPEC.md "Connection
// pool": keepalive every 15 s, three misses).
const (
	DefaultServerAliveInterval = 15 * time.Second
	DefaultServerAliveCountMax = 3
	DefaultConnectTimeout      = 15 * time.Second
)

// ErrProxyCommand is returned for aliases that use ProxyCommand.
var ErrProxyCommand = errors.New("ProxyCommand is not supported in this version; use ProxyJump")

// Env is the part of the operating system the resolver reads. Tests replace
// every field.
type Env struct {
	Home       string
	User       string // local user name, the default remote user
	ConfigPath string // the ssh config file; default ~/.ssh/config
	// SSHPath is the ssh binary used for "ssh -G". Empty means look it up on
	// PATH; "-" disables it and forces the library fallback.
	SSHPath string
	// Run executes a command and returns its stdout. Nil means os/exec.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// DefaultEnv reads the environment of the current process. The home
// directory comes from the password database, as OpenSSH does (it ignores
// $HOME), so the aliases Auriga lists and the config ssh -G resolves are the
// same file. $HOME is the fallback when the lookup fails.
func DefaultEnv() Env {
	env := Env{}
	if u, err := osuser.Current(); err == nil {
		env.Home, env.User = u.HomeDir, u.Username
	}
	if env.Home == "" {
		env.Home, _ = os.UserHomeDir()
	}
	if env.User == "" {
		env.User = os.Getenv("USER")
	}
	return env
}

func (e Env) configPath() string {
	if e.ConfigPath != "" {
		return e.ConfigPath
	}
	return filepath.Join(e.Home, ".ssh", "config")
}

func (e Env) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if e.Run != nil {
		return e.Run(ctx, name, args...)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed binary, alias validated by caller
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// Resolver turns aliases into HostConfigs.
type Resolver struct {
	env Env
	// Warning is set when the library fallback is in use, for the one-line
	// warning SPEC.md asks for.
	Warning string
	sshPath string
}

// NewResolver picks ssh -G when OpenSSH is installed and the library fallback
// otherwise.
func NewResolver(env Env) *Resolver {
	r := &Resolver{env: env}
	switch env.SSHPath {
	case "-":
	case "":
		if p, err := exec.LookPath("ssh"); err == nil {
			r.sshPath = p
		}
	default:
		r.sshPath = env.SSHPath
	}
	if r.sshPath == "" {
		r.Warning = "OpenSSH not found: Match blocks in ~/.ssh/config are ignored"
	}
	return r
}

// ErrBadAlias is returned for aliases that cannot be a concrete Host name.
var ErrBadAlias = errors.New("invalid host alias")

// ValidAlias guards an alias before it reaches argv, the library or a
// helper script: no whitespace, control characters, quotes, glob characters
// or leading dash.
func ValidAlias(alias string) error {
	if alias == "" || len(alias) > 255 || strings.HasPrefix(alias, "-") {
		return fmt.Errorf("%w %q", ErrBadAlias, alias)
	}
	for _, r := range alias {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`*?!"'\`+"`$;&|<>(){}[]", r) {
			return fmt.Errorf("%w %q", ErrBadAlias, alias)
		}
	}
	return nil
}

// Resolve returns the effective configuration for alias.
func (r *Resolver) Resolve(ctx context.Context, alias string) (HostConfig, error) {
	if err := ValidAlias(alias); err != nil {
		return HostConfig{}, err
	}
	var (
		h   HostConfig
		err error
	)
	if r.sshPath != "" {
		h, err = r.resolveSSHG(ctx, alias)
	} else {
		h, err = r.resolveLibrary(alias)
	}
	if err != nil {
		return HostConfig{}, err
	}
	if h.ProxyCommand != "" {
		return h, fmt.Errorf("%s: %w", alias, ErrProxyCommand)
	}
	return h, nil
}

func (r *Resolver) resolveSSHG(ctx context.Context, alias string) (HostConfig, error) {
	args := []string{"-G"}
	if r.env.ConfigPath != "" {
		args = append(args, "-F", r.env.ConfigPath)
	}
	args = append(args, "--", alias)
	out, err := r.env.run(ctx, r.sshPath, args...)
	if err != nil {
		return HostConfig{}, fmt.Errorf("ssh -G %s: %w", alias, err)
	}
	h, err := parseSSHG(out, r.env)
	if err != nil {
		return HostConfig{}, fmt.Errorf("ssh -G %s: %w", alias, err)
	}
	h.Alias = alias
	h.Source = "ssh -G"
	return h, nil
}

// parseSSHG reads "key value" lines from ssh -G. Keys are lower case;
// multi-valued keys repeat or hold space-separated lists.
func parseSSHG(out []byte, env Env) (HostConfig, error) {
	h := HostConfig{
		Port:                22,
		ServerAliveCountMax: DefaultServerAliveCountMax,
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		key, val, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		key = strings.ToLower(key)
		val = strings.TrimSpace(val)
		switch key {
		case "hostname":
			h.HostName = val
		case "user":
			h.User = val
		case "port":
			p, err := strconv.Atoi(val)
			if err != nil || p < 1 || p > 65535 {
				return h, fmt.Errorf("bad port %q", val)
			}
			h.Port = p
		case "identityfile":
			h.IdentityFiles = append(h.IdentityFiles, expandPath(val, env))
		case "identitiesonly":
			h.IdentitiesOnly = val == "yes"
		case "proxyjump":
			if val != "none" {
				h.ProxyJump = val
			}
		case "proxycommand":
			if val != "none" {
				h.ProxyCommand = val
			}
		case "userknownhostsfile":
			h.UserKnownHostsFiles = expandList(val, env)
		case "globalknownhostsfile":
			h.GlobalKnownHostsFiles = expandList(val, env)
		case "stricthostkeychecking":
			h.StrictHostKeyChecking = normalizeStrict(val)
		case "serveraliveinterval":
			h.ServerAliveInterval = parseSeconds(val)
		case "serveralivecountmax":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				h.ServerAliveCountMax = n
			}
		case "connecttimeout":
			h.ConnectTimeout = parseSeconds(val)
		}
	}
	if err := sc.Err(); err != nil {
		return h, err
	}
	if h.HostName == "" {
		return h, errors.New("no hostname in output")
	}
	applyDefaults(&h, env)
	return h, nil
}

func applyDefaults(h *HostConfig, env Env) {
	if h.User == "" {
		h.User = env.User
	}
	if h.ServerAliveInterval <= 0 {
		h.ServerAliveInterval = DefaultServerAliveInterval
	}
	if h.ServerAliveCountMax <= 0 {
		h.ServerAliveCountMax = DefaultServerAliveCountMax
	}
	if h.ConnectTimeout <= 0 {
		h.ConnectTimeout = DefaultConnectTimeout
	}
	if h.StrictHostKeyChecking == "" {
		h.StrictHostKeyChecking = "ask"
	}
	if len(h.UserKnownHostsFiles) == 0 {
		h.UserKnownHostsFiles = []string{
			filepath.Join(env.Home, ".ssh", "known_hosts"),
			filepath.Join(env.Home, ".ssh", "known_hosts2"),
		}
	}
	if len(h.GlobalKnownHostsFiles) == 0 {
		h.GlobalKnownHostsFiles = []string{"/etc/ssh/ssh_known_hosts", "/etc/ssh/ssh_known_hosts2"}
	}
	if len(h.IdentityFiles) == 0 {
		for _, n := range []string{"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk"} {
			h.IdentityFiles = append(h.IdentityFiles, filepath.Join(env.Home, ".ssh", n))
		}
	}
}

// normalizeStrict maps StrictHostKeyChecking values to "yes", "no", "ask"
// or "accept-new". ssh -G prints "yes" as "true" and "no" as "false" on
// OpenSSH 10.
func normalizeStrict(v string) string {
	switch strings.ToLower(v) {
	case "yes", "true":
		return "yes"
	case "no", "false", "off":
		return "no"
	case "accept-new":
		return "accept-new"
	default:
		return "ask"
	}
}

func parseSeconds(v string) time.Duration {
	if v == "none" || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

func expandList(v string, env Env) []string {
	var out []string
	for _, f := range strings.Fields(v) {
		out = append(out, expandPath(f, env))
	}
	return out
}

// expandPath expands ~ and the %d (home) and %u (local user) tokens. ssh -G
// already expands other tokens; the library fallback leaves them literal.
func expandPath(p string, env Env) string {
	if p == "~" {
		return env.Home
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(env.Home, p[2:])
	}
	p = strings.ReplaceAll(p, "%d", env.Home)
	p = strings.ReplaceAll(p, "%u", env.User)
	return p
}

// ParseProxyJump splits a ProxyJump value into hops in dialling order. Each
// hop is "[user@]host[:port]"; the host may be an alias.
func ParseProxyJump(v string) ([]JumpHop, error) {
	if v == "" || v == "none" {
		return nil, nil
	}
	var hops []JumpHop
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "ssh://")
		var hop JumpHop
		if u, rest, ok := strings.Cut(part, "@"); ok {
			hop.User, part = u, rest
		}
		if strings.HasPrefix(part, "[") {
			end := strings.Index(part, "]")
			if end < 0 {
				return nil, fmt.Errorf("bad ProxyJump hop %q", part)
			}
			hop.Host = part[1:end]
			part = part[end+1:]
			if p, ok := strings.CutPrefix(part, ":"); ok {
				n, err := strconv.Atoi(p)
				if err != nil {
					return nil, fmt.Errorf("bad ProxyJump port %q", p)
				}
				hop.Port = n
			}
		} else if h, p, ok := strings.Cut(part, ":"); ok {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("bad ProxyJump port %q", p)
			}
			hop.Host, hop.Port = h, n
		} else {
			hop.Host = part
		}
		if err := ValidAlias(hop.Host); err != nil {
			return nil, fmt.Errorf("bad ProxyJump hop: %w", err)
		}
		hops = append(hops, hop)
	}
	return hops, nil
}

// JumpHop is one entry of a ProxyJump list.
type JumpHop struct {
	User string
	Host string
	Port int // 0 means "from the host's own config"
}

// dedupe keeps the first occurrence of each string.
func dedupe(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
