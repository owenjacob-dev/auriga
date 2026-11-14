# Architecture

`SPEC.md` ("Architecture and tech stack") is the source of truth. This page
describes how the code is organised as it is built, and will grow with each
milestone.

## Process model

- `cmd/auriga` parses flags, resolves the config and state directories and
  runs one Bubble Tea program.
- `internal/app` holds the root model. It owns a stack of screens
  (`internal/ui.Screen`), the global keys (`?`, `q`, `esc`), the help overlay,
  the quit confirmation shown while any screen reports work in progress, and
  panic safety.
- Screens return a `ui.Layout` (subtitle, body lines, status line). The root
  model draws the frame, the UTC clock and the key hints around it, so every
  screen fits 80×24 the same way.
- `Update` never blocks or does I/O. Remote work will run in one worker
  goroutine per host (M1), and results come back as messages.

## Panic safety

Bubble Tea's own panic handler is turned off because it prints unredacted
panic values. The root model recovers panics in `Update`, `View` and in every
command it returns (batches are wrapped element by element). The first panic
is recorded, passed through `secret.Redact`, and the program quits. `main`
then writes `crash-<UTC>.txt` (mode 0600) to the state directory and prints
its path.

## Theme

`internal/ui/theme` builds styles for a dark or light background, using the
answer to Bubble Tea v2's background-colour query (dark until it answers).
With `NO_COLOR` set, no style carries a colour. Status is always a symbol and
a word (● ok, ◐ degraded, ○ down) as well as a colour.

## Local files

`internal/xdg` resolves `~/.config/auriga` and `~/.local/state/auriga`
(overridden by `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `--config`). Directories
are created 0700 and files are written atomically at 0600.
