package stellarcfg

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/owenjacob-dev/auriga/internal/strkey"
)

// Severity of a validation issue.
type Severity int

const (
	// Warn is shown but does not block a deploy.
	Warn Severity = iota
	// Block stops a deploy until fixed.
	Block
)

func (s Severity) String() string {
	if s == Block {
		return "blocking"
	}
	return "warning"
}

// Issue is one validation finding (SPEC.md "Errors": rule name and the
// offending entry).
type Issue struct {
	Rule     string
	Key      string // the key or entry, e.g. "HTTP_PORT" or "[[VALIDATORS]] acme-b"
	Message  string
	Severity Severity
}

func (i Issue) String() string { return fmt.Sprintf("%s (%s): %s", i.Key, i.Rule, i.Message) }

// ValidateOptions describe the host the config is for.
type ValidateOptions struct {
	// ServiceWorkDir is where the service runs, from the probe: relative
	// paths resolve against it. "/" is the packaged unit's default; empty
	// means unknown.
	ServiceWorkDir string
}

var (
	hostnameRE = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	archiveRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

// ValidHostname reports whether s is a fully qualified host name, as
// NODE_HOME_DOMAIN must be.
func ValidHostname(s string) bool { return len(s) <= 253 && hostnameRE.MatchString(s) }

// Validate checks everything that does not need the rest of the quorum
// (quorum-wide rules live in internal/quorum).
func Validate(c *Config, opt ValidateOptions) []Issue {
	var out []Issue
	add := func(sev Severity, rule, key, format string, args ...any) {
		out = append(out, Issue{Rule: rule, Key: key, Message: fmt.Sprintf(format, args...), Severity: sev})
	}

	if c.PublicHTTPPort != nil && *c.PublicHTTPPort {
		add(Block, "public-http-port", "PUBLIC_HTTP_PORT", "is true: the admin endpoint must never be reachable from the network")
	}
	if c.NetworkPassphrase == "" {
		add(Block, "network-passphrase", "NETWORK_PASSPHRASE", "is missing")
	}
	isValidator := c.NodeIsValidator != nil && *c.NodeIsValidator
	if isValidator && c.NodeSeed == nil {
		add(Block, "validator-seed", "NODE_SEED", "is missing, but NODE_IS_VALIDATOR is true")
	}
	if c.NodeSeed != nil && c.NodeSeed.PublicKey != "" && !strkey.ValidPublicKey(c.NodeSeed.PublicKey) {
		add(Block, "seed-public-key", "NODE_SEED", "derived public key %q is not a valid G… key", c.NodeSeed.PublicKey)
	}
	if c.NodeHomeDomain != "" && !ValidHostname(c.NodeHomeDomain) {
		add(Block, "home-domain", "NODE_HOME_DOMAIN", "%q is not a valid host name", c.NodeHomeDomain)
	}

	port := func(k string, p *int) {
		if p != nil && (*p < 1 || *p > 65535) {
			add(Block, "port-range", k, "%d is not a port number (1-65535)", *p)
		}
	}
	port("HTTP_PORT", c.HTTPPort)
	port("PEER_PORT", c.PeerPort)
	if c.HTTPPort != nil && c.PeerPort != nil && *c.HTTPPort == *c.PeerPort {
		add(Block, "port-clash", "PEER_PORT", "is the same as HTTP_PORT (%d)", *c.PeerPort)
	}

	switch {
	case c.Database == "":
		add(Warn, "database-missing", "DATABASE", "is not set; stellar-core falls back to an in-memory database")
	case !strings.HasPrefix(c.Database, "sqlite3://") && !strings.HasPrefix(c.Database, "postgresql://"):
		add(Block, "database-scheme", "DATABASE", "must start with sqlite3:// or postgresql://")
	}

	for _, peer := range append(append([]string{}, c.KnownPeers...), c.PreferredPeers...) {
		if !validPeer(peer) {
			add(Block, "peer-address", "KNOWN_PEERS/PREFERRED_PEERS", "%q is not host or host:port", peer)
		}
	}
	for _, k := range c.PreferredPeerKeys {
		if !strkey.ValidPublicKey(k) && !validName(k) {
			add(Block, "peer-key", "PREFERRED_PEER_KEYS", "%q is neither a G… key nor a validator name", k)
		}
	}

	out = append(out, validateQuorumEntries(c)...)
	out = append(out, validateHistory(c)...)
	out = append(out, relativePaths(c, opt)...)
	return out
}

func validName(s string) bool { return s != "" && !strings.ContainsAny(s, " \t\"") }

func validPeer(s string) bool {
	host, p, err := net.SplitHostPort(s)
	if err != nil {
		host, p = s, ""
	}
	if host == "" || strings.ContainsAny(host, " /") {
		return false
	}
	if p != "" {
		n, err := strconv.Atoi(p)
		return err == nil && n >= 1 && n <= 65535
	}
	return true
}

func validateQuorumEntries(c *Config) []Issue {
	var out []Issue
	add := func(rule, key, format string, args ...any) {
		out = append(out, Issue{Rule: rule, Key: key, Message: fmt.Sprintf(format, args...), Severity: Block})
	}
	domains := map[string]bool{}
	for _, h := range c.HomeDomains {
		k := "[[HOME_DOMAINS]] " + h.HomeDomain
		if !ValidHostname(h.HomeDomain) {
			add("home-domain", k, "HOME_DOMAIN %q is not a valid host name", h.HomeDomain)
		}
		if domains[h.HomeDomain] {
			add("duplicate-home-domain", k, "is listed twice")
		}
		domains[h.HomeDomain] = true
		if !h.Quality.Valid() {
			add("quality", k, "QUALITY %q must be CRITICAL, HIGH, MEDIUM or LOW", h.Quality)
		}
	}
	names, keys := map[string]bool{}, map[string]bool{}
	for _, v := range c.Validators {
		k := "[[VALIDATORS]] " + v.Name
		switch {
		case v.Name == "":
			add("validator-name", "[[VALIDATORS]]", "an entry has no NAME")
		case names[v.Name]:
			add("duplicate-name", k, "NAME is used twice")
		}
		names[v.Name] = true
		if c.NodeSeed != nil && v.Name != "" && v.Name == c.NodeSeed.Alias {
			add("name-is-self", k, "NAME is this node's own alias from NODE_SEED; stellar-core refuses to start (list siblings only)")
		}
		switch {
		case !strkey.ValidPublicKey(v.PublicKey):
			add("public-key", k, "PUBLIC_KEY %q is not a valid G… key", v.PublicKey)
		case keys[v.PublicKey]:
			add("duplicate-key", k, "PUBLIC_KEY is used twice")
		}
		keys[v.PublicKey] = true
		if v.HomeDomain == "" {
			add("validator-home-domain", k, "has no HOME_DOMAIN")
		}
		if v.Quality != "" && !v.Quality.Valid() {
			add("quality", k, "QUALITY %q must be CRITICAL, HIGH, MEDIUM or LOW", v.Quality)
		}
		if v.Address != "" && !validPeer(v.Address) {
			add("validator-address", k, "ADDRESS %q is not host or host:port", v.Address)
		}
		if v.History != "" && !strings.Contains(v.History, "{0}") {
			add("history-template", k, "HISTORY must contain {0}")
		}
	}
	return out
}

func validateHistory(c *Config) []Issue {
	var out []Issue
	add := func(rule, key, format string, args ...any) {
		out = append(out, Issue{Rule: rule, Key: key, Message: fmt.Sprintf(format, args...), Severity: Block})
	}
	seen := map[string]bool{}
	for _, h := range c.History {
		k := "[HISTORY." + h.Name + "]"
		if !archiveRE.MatchString(h.Name) {
			add("archive-name", k, "name may only use letters, digits, '.', '_' and '-'")
		}
		if seen[h.Name] {
			add("duplicate-archive", k, "is defined twice")
		}
		seen[h.Name] = true
		if h.Get == "" {
			add("history-get", k, "has no get command")
		} else if !strings.Contains(h.Get, "{0}") || !strings.Contains(h.Get, "{1}") {
			add("history-template", k, "get must contain {0} and {1}")
		}
		if h.Put != "" && (!strings.Contains(h.Put, "{0}") || !strings.Contains(h.Put, "{1}")) {
			add("history-template", k, "put must contain {0} and {1}")
		}
		if h.Mkdir != "" && !strings.Contains(h.Mkdir, "{0}") {
			add("history-template", k, "mkdir must contain {0}")
		}
	}
	return out
}

// relativePaths warns about paths that resolve against the service's
// working directory when that is "/" (the packaged unit), where stellar
// cannot write: stellar-core then fails to start (docs/decisions.md).
func relativePaths(c *Config, opt ValidateOptions) []Issue {
	if opt.ServiceWorkDir != "/" {
		return nil
	}
	var out []Issue
	warn := func(k, what string) {
		out = append(out, Issue{Rule: "relative-path", Key: k, Severity: Warn,
			Message: what + " resolves against / because the service sets no WorkingDirectory; use an absolute path"})
	}
	switch {
	case c.LogFilePath == nil:
		warn("LOG_FILE_PATH", "the default log file path")
	case *c.LogFilePath != "" && !path.IsAbs(*c.LogFilePath):
		warn("LOG_FILE_PATH", fmt.Sprintf("%q", *c.LogFilePath))
	}
	switch {
	case c.BucketDirPath == "":
		warn("BUCKET_DIR_PATH", "the default bucket directory")
	case !path.IsAbs(c.BucketDirPath):
		warn("BUCKET_DIR_PATH", fmt.Sprintf("%q", c.BucketDirPath))
	}
	if p, ok := strings.CutPrefix(c.Database, "sqlite3://"); ok && !path.IsAbs(p) {
		warn("DATABASE", fmt.Sprintf("the SQLite file %q", p))
	}
	return out
}

// Blocking returns the blocking issues.
func Blocking(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.Severity == Block {
			out = append(out, i)
		}
	}
	return out
}
