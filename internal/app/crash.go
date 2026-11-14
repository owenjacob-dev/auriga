package app

import (
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/secret"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// Crash records the first panic caught anywhere in the UI. It is shared by
// pointer between the root model's copies, because View cannot return a
// changed model.
type Crash struct {
	mu      sync.Mutex
	value   string
	stack   string
	at      time.Time
	onCrash func()
}

// NewCrash returns an empty crash holder. onCrash, if set, runs (once, in its
// own goroutine) after the first panic is recorded; main uses it to quit the
// program when a panic happens inside View.
func NewCrash(onCrash func()) *Crash { return &Crash{onCrash: onCrash} }

// SetOnCrash sets the callback after construction (the program does not exist
// yet when the model is built).
func (c *Crash) SetOnCrash(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onCrash = f
}

func (c *Crash) record(r any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != "" {
		return
	}
	c.value = secret.Redact(fmt.Sprint(r))
	if c.value == "" {
		c.value = "panic with empty value"
	}
	c.stack = secret.Redact(string(debug.Stack()))
	c.at = time.Now().UTC()
	if c.onCrash != nil {
		go c.onCrash()
	}
}

// Happened reports whether a panic was recorded.
func (c *Crash) Happened() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value != ""
}

// Report returns the redacted crash report text.
func (c *Crash) Report(version string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "auriga crash report\n")
	fmt.Fprintf(&b, "time:    %s\n", c.at.Format(time.RFC3339))
	fmt.Fprintf(&b, "version: %s\n", version)
	fmt.Fprintf(&b, "go:      %s %s/%s\n\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "panic: %s\n\n%s", c.value, c.stack)
	return secret.Redact(b.String())
}

// WriteReport writes the redacted report into stateDir with owner-only
// permissions and returns its path.
func (c *Crash) WriteReport(stateDir, version string) (string, error) {
	c.mu.Lock()
	at := c.at
	c.mu.Unlock()
	name := filepath.Join(stateDir, "crash-"+at.Format("20060102T150405Z")+".txt")
	if err := xdg.WriteFile(name, []byte(c.Report(version))); err != nil {
		return "", err
	}
	return name, nil
}

// Recover records a panic from the calling goroutine. Use as
// `defer crash.Recover()` at the top of goroutines Auriga starts.
func (c *Crash) Recover() {
	if r := recover(); r != nil {
		c.record(r)
	}
}

// crashMsg tells the root model that a command panicked.
type crashMsg struct{}

// safeCmd wraps cmd so a panic inside it is recorded and turned into a
// crashMsg instead of killing the program with an unredacted stack trace.
// Batches are wrapped element by element.
func (c *Crash) safeCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				c.record(r)
				msg = crashMsg{}
			}
		}()
		msg = cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			wrapped := make(tea.BatchMsg, len(batch))
			for i, sub := range batch {
				wrapped[i] = c.safeCmd(sub)
			}
			return wrapped
		}
		return msg
	}
}
