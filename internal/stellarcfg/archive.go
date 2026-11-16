package stellarcfg

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Backend is a history archive backend (SPEC.md "Step 6: history archive").
type Backend string

const (
	// BackendNginx is a local directory served over HTTPS by a web server.
	BackendNginx Backend = "nginx"
	// BackendS3 is S3-compatible object storage (S3, R2, B2).
	BackendS3 Backend = "s3"
	// BackendCustom takes operator-supplied commands.
	BackendCustom Backend = "custom"
)

// ArchiveSpec is what the wizard asks for one node's archive.
type ArchiveSpec struct {
	Name    string // [HISTORY.<name>], also the new-hist argument
	Backend Backend
	// URL is the archive's public base URL, e.g. https://history-a.acme.org.
	URL string
	// Dir is the local directory the web server serves (nginx).
	Dir string
	// Bucket, Prefix and Endpoint locate an S3-compatible archive; Endpoint
	// is set for R2 or B2.
	Bucket, Prefix, Endpoint string
	// Get, Put and Mkdir are used as given for BackendCustom.
	Get, Put, Mkdir string
}

var (
	safeURL    = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
	safePath   = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	safeBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	safePrefix = regexp.MustCompile(`^[A-Za-z0-9._/-]*$`)
	// unsafeShell are characters that would let a value change the meaning
	// of a command template stellar-core runs with a shell.
	unsafeShell = "`$;&|<>(){}\\\"'\n\r\t"
)

// Validate checks the spec. Every value ends up inside a shell command that
// stellar-core runs, so the guided backends accept only plain URLs, paths,
// bucket names and prefixes.
func (a ArchiveSpec) Validate() error {
	if !archiveRE.MatchString(a.Name) {
		return fmt.Errorf("archive name %q may only use letters, digits, '.', '_' and '-'", a.Name)
	}
	switch a.Backend {
	case BackendNginx, BackendS3:
		if !safeURL.MatchString(a.URL) {
			return fmt.Errorf("archive URL %q must be a plain http(s) URL", a.URL)
		}
	}
	switch a.Backend {
	case BackendNginx:
		if !safePath.MatchString(a.Dir) || strings.Contains(a.Dir, "..") {
			return fmt.Errorf("directory %q must be an absolute path without spaces or '..'", a.Dir)
		}
	case BackendS3:
		if !safeBucket.MatchString(a.Bucket) {
			return fmt.Errorf("bucket %q is not a valid bucket name", a.Bucket)
		}
		if !safePrefix.MatchString(a.Prefix) || strings.Contains(a.Prefix, "..") {
			return fmt.Errorf("prefix %q may only use letters, digits, '.', '_', '-' and '/'", a.Prefix)
		}
		if a.Endpoint != "" && !safeURL.MatchString(a.Endpoint) {
			return fmt.Errorf("endpoint %q must be a plain https URL", a.Endpoint)
		}
	case BackendCustom:
		if a.Get == "" {
			return fmt.Errorf("a get command is required")
		}
		for name, cmd := range map[string]string{"get": a.Get, "put": a.Put, "mkdir": a.Mkdir} {
			if strings.ContainsAny(cmd, "\n\r") {
				return fmt.Errorf("%s must be a single line", name)
			}
		}
	default:
		return fmt.Errorf("unknown backend %q", a.Backend)
	}
	return nil
}

// History returns the [HISTORY.<name>] table for the spec. publish is false
// for node types that only read archives (no put or mkdir).
func (a ArchiveSpec) History(publish bool) (HistoryArchive, error) {
	if err := a.Validate(); err != nil {
		return HistoryArchive{}, err
	}
	h := HistoryArchive{Name: a.Name, Get: a.GetTemplate()}
	if !publish {
		return h, nil
	}
	switch a.Backend {
	case BackendNginx:
		dir := strings.TrimRight(a.Dir, "/")
		h.Put = "cp {0} " + dir + "/{1}"
		h.Mkdir = "mkdir -p " + dir + "/{0}"
	case BackendS3:
		h.Put = "aws s3 cp {0} s3://" + path.Join(a.Bucket, a.Prefix) + "/{1}"
		if a.Endpoint != "" {
			h.Put += " --endpoint-url " + a.Endpoint
		}
	case BackendCustom:
		h.Put, h.Mkdir = a.Put, a.Mkdir
	}
	return h, nil
}

// GetTemplate is the public get command: what siblings put in this node's
// [[VALIDATORS]] HISTORY.
func (a ArchiveSpec) GetTemplate() string {
	if a.Backend == BackendCustom {
		return a.Get
	}
	return "curl -sf " + strings.TrimRight(a.URL, "/") + "/{0} -o {1}"
}

// PutTarget identifies where the archive is written, so two hosts never
// publish to the same place (SPEC.md: one archive per node).
func (a ArchiveSpec) PutTarget() string {
	switch a.Backend {
	case BackendS3:
		return "s3://" + path.Join(a.Bucket, a.Prefix) + "@" + a.Endpoint
	case BackendCustom:
		return a.Put
	}
	return strings.TrimRight(a.URL, "/")
}

// Binaries lists the programs the archive's commands run, which must exist
// for the stellar user on the host.
func (a ArchiveSpec) Binaries(publish bool) []string {
	h, err := a.History(publish)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, cmd := range []string{h.Get, h.Put, h.Mkdir} {
		if f := strings.Fields(cmd); len(f) > 0 && !seen[f[0]] && !strings.ContainsAny(f[0], unsafeShell) {
			seen[f[0]] = true
			out = append(out, path.Base(f[0]))
		}
	}
	return out
}

// WellKnownURL is where an existing archive answers.
func (a ArchiveSpec) WellKnownURL() string {
	return strings.TrimRight(a.URL, "/") + "/.well-known/stellar-history.json"
}
