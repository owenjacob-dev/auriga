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

## SSH layer (`internal/sshx`)

- `ListAliases` reads concrete `Host` names from `~/.ssh/config`, following
  `Include`. `Resolver` resolves an alias with `ssh -G` (or
  `kevinburke/ssh_config` without OpenSSH). The home directory comes from the
  password database, as OpenSSH's does.
- `Dialer` authenticates with agent keys, then each `IdentityFile`. An
  encrypted key asks for its passphrase only when the server accepts the key.
  Host keys are checked against known_hosts; unknown keys go to the operator
  through the `Prompter`. `ProxyJump` hops are dialled through the previous
  client, each with the same checks.
- `Pool` runs one worker goroutine per host. The worker owns the connection,
  sends keepalives, reconnects with backoff, and serves `Run` requests with
  at most four channels open. It reports `StateEvent`s and `PromptEvent`s on
  one channel the UI reads.
- `sshtest` is an in-process SSH server for tests.

## Remote helpers (`internal/remote`)

Every remote action is an embedded script run as
`sudo -n -- sh -s -- 'arg'…` (or `sudo -k -S -p '' …` with the password as
the first stdin line). The script follows on stdin, wrapped in a function so
`sh` parses it whole before running it. A secret is written only after the
helper asks for it with `auriga_read_secret`; see `docs/decisions.md` for why
(dash reads piped scripts in blocks). Arguments are allowlist-checked, then
single-quoted.

Read-only helpers: `probe.sh` (host facts as `key=value` lines),
`read-config.sh` (the config with `NODE_SEED` replaced on the host by
`<redacted:G…>`), `admin-get.sh`, `list-backups.sh` and `check-archive.sh`.
Deploy helpers: `preflight.sh`, `gen-seed.sh`, `stage.sh` (the render arrives
as a base64 argument; the seed is taken from the current config, the pending
generated seed, or stdin, and substituted with shell built-ins), `stage-backup.sh`,
`backup.sh`, `install.sh`, `init.sh`, `restart.sh` and `rollback.sh`.

## Admin endpoint (`internal/corehttp`)

`corehttp.Client` can build only the six read-only commands. A `Transport`
carries each request: the SSH tunnel (a direct-tcpip channel to
`127.0.0.1:HTTP_PORT`, one pool slot per request) or, once sshd refuses
forwarding, the `admin-get.sh` helper on the host. `corehttp.Adaptive`
switches between them. Response types are lenient across core builds;
`testdata/core/recorded/` pins field names to real responses.

## Config model (`internal/stellarcfg`)

`Parse` walks go-toml's AST: owned keys become typed fields, everything else
becomes a verbatim `Entry`. `Render` writes a fixed layout from the model and
a `RenderOptions` time, so the same input always gives the same bytes;
`NODE_SEED` is a `<auriga-seed:…>` marker that only the host helper replaces.
`Validate` returns blocking issues and warnings; `Classify` decides
unconfigured, adoptable or read-only. The root model runs both after every
probe.

## Quorum, presets and the wizard

`internal/quorum` mirrors stellar-core's quorum-set generation (orgs need a
simple majority; quality levels nest with 67% thresholds) and its safety
rule, and is tested against sets stellar-core 30 generated. `internal/presets`
embeds the testnet and pubnet presets and handles refresh overrides.
`internal/ui/wizard` holds the pure `Draft` and `BuildPlan` (answers in,
per-host configs, actions and issues out) and the screen that edits a draft
step by step. The plan's configs are what the deploy flow (M5) stages; keys
generated on hosts are pending until then.

## Deploys (`internal/deploy`, `internal/audit`)

The coordinator runs a reviewed plan host by host through an `Ops` interface
(implemented in `internal/app` with the helpers and the admin client) and
reports `Event`s, which the root model feeds to the deploy screen. It never
touches the UI and the UI never blocks on it. Failures before install leave a
host unchanged; from install on, the backup is restored. Every step that
changes a host appends a redacted JSON line to the audit log. Manual rollback
is the same run with a backup staged instead of a render.

## Network upgrades (`internal/upgrades`)

A pure package: the `upgrades?mode=set` parameters, the parser for SDF's
arming strings, the query builder, the checks before sending, the read-back
of `upgrades?mode=get`, and the Soroban settings comparison. The upgrades
screen (`internal/ui/upgradesview`) asks the root model for every request;
the root model sends them through each host's admin client, writes the
audit entries, and follows armed votes on each `info` poll.

## Polling and health

The root model polls each connected host's `info` on its own `tea.Tick`
timer, with a generation number per connection so stale timers are dropped.
`internal/health` turns the samples into dashboard cells and plain-language
problems; it is pure and table-tested.

## Root model and fleet

The root model owns the inventory and a `ui.Fleet` (one `HostView` per host).
It talks to the outside world through the `app.Backend` interface, only from
commands. Screens read the fleet and return intents (`AddHostsMsg`,
`RemoveHostMsg`, `RefreshHostMsg`, …); prompts queue as an overlay above any
screen. Each host is probed whenever its connection comes up.

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
