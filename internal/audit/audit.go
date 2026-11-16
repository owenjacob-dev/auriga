// Package audit appends one JSON object per mutating action to the audit
// log (SPEC.md "Local state and files") and reads it back for the log
// screen. Every string is passed through secret.Redact before it is
// written, so a seed that slips into an error message never reaches disk.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// FileName is the log's name in the state directory.
const FileName = "audit.log"

// Results.
const (
	OK     = "ok"
	Failed = "failed"
)

// Entry is one line of the log.
type Entry struct {
	Time         time.Time `json:"ts"`
	Host         string    `json:"host"`
	Org          string    `json:"org,omitempty"`
	Network      string    `json:"network,omitempty"`
	Action       string    `json:"action"` // e.g. "deploy.install"
	ConfigSHA256 string    `json:"config_sha256,omitempty"`
	Backup       string    `json:"backup,omitempty"`
	PublicKey    string    `json:"public_key,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Result       string    `json:"result"`
	Error        string    `json:"error,omitempty"`
}

func (e Entry) redacted() Entry {
	for _, s := range []*string{&e.Host, &e.Org, &e.Network, &e.Action, &e.ConfigSHA256, &e.Backup, &e.PublicKey, &e.Detail, &e.Result, &e.Error} {
		*s = secret.Redact(*s)
	}
	e.Time = e.Time.UTC().Truncate(time.Second)
	return e
}

// Log appends entries to one file. It is safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// Open returns the log in stateDir. The file is created on the first write.
func Open(stateDir string) *Log {
	return &Log{path: filepath.Join(stateDir, FileName), now: time.Now}
}

// Path is the log file.
func (l *Log) Path() string { return l.path }

// Append writes e, setting its time when zero. The file is 0600 in a 0700
// directory and each entry is written and synced in one write.
func (l *Log) Append(e Entry) error {
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	if e.Host == "" || e.Action == "" || e.Result == "" {
		return errors.New("audit entry needs a host, an action and a result")
	}
	b, err := json.Marshal(e.redacted())
	if err != nil {
		return err
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := xdg.EnsureDir(filepath.Dir(l.path)); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, xdg.FileMode)
	if err != nil {
		return err
	}
	if fi, err := f.Stat(); err == nil && fi.Mode().Perm() != xdg.FileMode {
		f.Chmod(xdg.FileMode) //nolint:errcheck,gosec // best effort tightening
	}
	if _, err := f.Write(b); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close() //nolint:errcheck,gosec
		return err
	}
	return f.Close()
}

// Read returns the log's entries, newest first (the reverse of the order
// they were appended). Lines that do not parse
// are skipped and counted.
func (l *Log) Read() ([]Entry, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close() //nolint:errcheck
	var out []Entry
	bad := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if len(sc.Bytes()) == 0 {
			continue
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			bad++
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, bad, fmt.Errorf("reading %s: %w", l.path, err)
	}
	// Lines are appended in order; times are whole seconds, so several
	// entries can share one.
	slices.Reverse(out)
	return out, bad, nil
}

// Filter keeps entries for host (any when empty) whose action starts with
// action (any when empty).
func Filter(entries []Entry, host, action string) []Entry {
	var out []Entry
	for _, e := range entries {
		if (host == "" || e.Host == host) && (action == "" || hasPrefix(e.Action, action)) {
			out = append(out, e)
		}
	}
	return out
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
