package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/owenjacob-dev/auriga/internal/strkey"
)

// Default host paths (SPEC.md "Assumptions about each host").
const (
	DefaultConfigPath = "/etc/stellar/stellar-core.cfg"
	DefaultService    = "stellar-core"
)

// ProbeFacts is what probe.sh reports about a host.
type ProbeFacts struct {
	Hostname       string
	OS             string
	StellarUser    bool
	CoreInstalled  bool
	CoreVersion    string // first line of `stellar-core version`
	ServiceLoad    string // "loaded", "not-found", …
	ServiceActive  string // "active", "inactive", "failed", …
	ServiceEnabled string // "enabled", "disabled", …
	ConfigPresent  bool
	ConfigReadable bool
	ConfigMode     string // octal, e.g. "640"
	ConfigOwner    string // "root:stellar"
	ConfigSHA256   string
	HTTPPort       int    // from the config; 0 when unset
	ServiceWorkDir string // where the service runs; relative config paths start here
	BucketDir      string
	DiskTotalKB    int64
	DiskAvailKB    int64
	NTPSync        string // "yes", "no" or "unknown"
}

// WorldReadable reports whether the config mode lets anyone read it
// (SPEC.md "Secrets": warn about world-readable configs).
func (f ProbeFacts) WorldReadable() bool {
	if len(f.ConfigMode) == 0 {
		return false
	}
	last := f.ConfigMode[len(f.ConfigMode)-1]
	return last >= '0' && last <= '7' && (last-'0')&4 != 0
}

// Probe runs probe.sh.
func (r *Runner) Probe(ctx context.Context, alias, configPath, service string) (ProbeFacts, error) {
	res, err := r.Run(ctx, alias, HelperProbe, []Arg{{ArgPath, configPath}, {ArgName, service}}, RunOptions{})
	if err != nil {
		return ProbeFacts{}, err
	}
	return parseProbe(res.Stdout)
}

func parseProbe(out []byte) (ProbeFacts, error) {
	var f ProbeFacts
	kv, _ := parseKV(out)
	if len(kv) == 0 {
		return f, errors.New("probe printed nothing")
	}
	yes := func(k string) bool { return kv[k] == "yes" }
	num := func(k string) int64 { n, _ := strconv.ParseInt(kv[k], 10, 64); return n }
	f.Hostname = kv["hostname"]
	f.OS = kv["os"]
	f.StellarUser = yes("stellar_user")
	f.CoreInstalled = yes("core_installed")
	f.CoreVersion = kv["core_version"]
	f.ServiceLoad = kv["service_load"]
	f.ServiceActive = kv["service_active"]
	f.ServiceEnabled = kv["service_enabled"]
	f.ConfigPresent = yes("config_present")
	f.ConfigReadable = yes("config_readable")
	f.ConfigMode = kv["config_mode"]
	f.ConfigOwner = kv["config_owner"]
	f.ConfigSHA256 = kv["config_sha256"]
	f.HTTPPort = int(num("http_port"))
	f.ServiceWorkDir = kv["service_workdir"]
	f.BucketDir = kv["bucket_dir"]
	f.DiskTotalKB = num("disk_total_kb")
	f.DiskAvailKB = num("disk_avail_kb")
	f.NTPSync = kv["ntp_synchronized"]
	if f.NTPSync == "" {
		f.NTPSync = "unknown"
	}
	return f, nil
}

// parseKV reads key=value lines up to a "---" line and returns the map and
// the bytes after the separator.
func parseKV(out []byte) (map[string]string, []byte) {
	kv := map[string]string{}
	rest := out
	for len(rest) > 0 {
		line := rest
		next := []byte(nil)
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, next = rest[:i], rest[i+1:]
		}
		if string(line) == "---" {
			return kv, next
		}
		if k, v, ok := strings.Cut(string(line), "="); ok {
			kv[k] = v
		}
		rest = next
	}
	return kv, nil
}

// SeedState says whether a config has a NODE_SEED line.
type SeedState string

const (
	SeedPresent SeedState = "present"
	SeedAbsent  SeedState = "absent"
)

// RedactedConfig is a config read with its seed redacted on the host.
type RedactedConfig struct {
	PublicKey string // derived on the host; empty if derivation failed
	Seed      SeedState
	NodeAlias string
	Text      []byte // the file with NODE_SEED = "<redacted:G…[ alias]>"
}

// ErrNoConfig is returned when the config file does not exist.
var ErrNoConfig = errors.New("config file not found")

// ReadConfig runs read-config.sh.
func (r *Runner) ReadConfig(ctx context.Context, alias, configPath string) (RedactedConfig, error) {
	res, err := r.Run(ctx, alias, HelperReadConfig, []Arg{{ArgPath, configPath}}, RunOptions{})
	var he *Error
	if errors.As(err, &he) && he.Exit == 3 {
		return RedactedConfig{}, fmt.Errorf("%s: %s: %w", alias, configPath, ErrNoConfig)
	}
	if err != nil {
		return RedactedConfig{}, err
	}
	return parseReadConfig(res.Stdout)
}

func parseReadConfig(out []byte) (RedactedConfig, error) {
	kv, body := parseKV(out)
	if body == nil && !bytes.Contains(out, []byte("\n---\n")) {
		return RedactedConfig{}, errors.New("read-config output has no separator")
	}
	rc := RedactedConfig{
		PublicKey: kv["pubkey"],
		Seed:      SeedState(kv["seed"]),
		NodeAlias: kv["node_alias"],
		Text:      body,
	}
	if rc.PublicKey != "" && !strkey.ValidPublicKey(rc.PublicKey) {
		return RedactedConfig{}, fmt.Errorf("host returned an invalid public key %q", rc.PublicKey)
	}
	return rc, nil
}

// AdminResponse is the result of an admin request made on the host.
type AdminResponse struct {
	Status int
	Body   []byte
	Via    string // "curl" or "http-command"
}

// AdminGet requests cmd (e.g. "quorum?compact=false") from the admin
// endpoint by running curl, or stellar-core http-command, on the host.
func (r *Runner) AdminGet(ctx context.Context, alias string, port int, cmd, configPath, service string) (AdminResponse, error) {
	res, err := r.Run(ctx, alias, HelperAdminGet, []Arg{
		{ArgPort, strconv.Itoa(port)}, {ArgAdmin, cmd}, {ArgPath, configPath}, {ArgName, service},
	}, RunOptions{})
	if err != nil {
		return AdminResponse{}, err
	}
	return parseAdminGet(res.Stdout)
}

func parseAdminGet(out []byte) (AdminResponse, error) {
	out = bytes.TrimRight(out, "\n")
	i := bytes.LastIndex(out, []byte("\n@@status="))
	var trailer []byte
	switch {
	case i >= 0:
		trailer, out = out[i+1:], out[:i]
	case bytes.HasPrefix(out, []byte("@@status=")):
		trailer, out = out, nil
	default:
		return AdminResponse{}, errors.New("admin-get printed no status line")
	}
	var r AdminResponse
	for _, f := range strings.Fields(string(trailer)) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "@@status":
			n, err := strconv.Atoi(v)
			if err != nil {
				return AdminResponse{}, fmt.Errorf("bad status %q", v)
			}
			r.Status = n
		case "via":
			r.Via = v
		}
	}
	if r.Via == "http-command" {
		out = stripHTTPHeaders(out)
	}
	r.Body = out
	return r, nil
}

// stripHTTPHeaders removes the header block `stellar-core http-command`
// prints before the body on current builds ("Content-Length: …",
// "Content-Type: …", blank line). Output without headers is unchanged.
func stripHTTPHeaders(out []byte) []byte {
	if !bytes.HasPrefix(out, []byte("Content-")) && !bytes.HasPrefix(out, []byte("HTTP/")) {
		return out
	}
	for _, sep := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if i := bytes.Index(out, sep); i >= 0 {
			return out[i+len(sep):]
		}
	}
	return out
}
