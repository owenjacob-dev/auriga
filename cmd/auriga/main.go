// Command auriga is a terminal UI for configuring, deploying and monitoring
// Stellar validators over SSH. See SPEC.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	tea "charm.land/bubbletea/v2"

	"github.com/owenjacob-dev/auriga/internal/app"
	"github.com/owenjacob-dev/auriga/internal/xdg"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

type flags struct {
	configDir string
	debug     bool
	version   bool
}

func parseFlags(args []string, stderr io.Writer) (flags, error) {
	var f flags
	fs := flag.NewFlagSet("auriga", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.configDir, "config", "", "config `directory` (default $XDG_CONFIG_HOME/auriga or ~/.config/auriga)")
	fs.BoolVar(&f.debug, "debug", false, "write a redacted debug log to the state directory")
	fs.BoolVar(&f.version, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: auriga [flags]\n\nConfigure, deploy and monitor Stellar validators over SSH.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return f, nil
}

func versionString() string {
	return fmt.Sprintf("auriga %s (%s, %s/%s)", version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	f, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "auriga:", err)
		return 2
	}
	if f.version {
		fmt.Fprintln(stdout, versionString())
		return 0
	}

	dirs, err := xdg.Resolve(os.Getenv, f.configDir)
	if err != nil {
		fmt.Fprintln(stderr, "auriga:", err)
		return 1
	}

	crash := app.NewCrash(nil)
	model := app.New(app.Options{
		Version: version,
		NoColor: os.Getenv("NO_COLOR") != "",
		Crash:   crash,
	})
	// Bubble Tea's own panic handler prints the panic value and stack to the
	// terminal unredacted; the root model catches panics instead.
	p := tea.NewProgram(model, tea.WithoutCatchPanics())
	crash.SetOnCrash(p.Quit)

	_, runErr := runProgram(p, crash)

	if crash.Happened() {
		path, err := crash.WriteReport(dirs.State, version)
		if err != nil {
			fmt.Fprintln(stderr, "auriga stopped after an internal error, and the crash report could not be written:", err)
			return 3
		}
		fmt.Fprintf(stderr, "auriga stopped after an internal error. A redacted crash report is at %s\n", path)
		return 3
	}
	if runErr != nil {
		fmt.Fprintln(stderr, "auriga:", runErr)
		return 1
	}
	return 0
}

// runProgram runs p and turns any panic that escapes the model's own guards
// into a recorded crash, after restoring the terminal.
func runProgram(p *tea.Program, crash *app.Crash) (m tea.Model, err error) {
	defer func() {
		if r := recover(); r != nil {
			p.Kill() // restores the terminal
			func() {
				defer crash.Recover()
				panic(r)
			}()
			err = tea.ErrProgramPanic
		}
	}()
	return p.Run()
}
