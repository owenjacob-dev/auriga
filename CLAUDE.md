# CLAUDE.md

Auriga is an open-source Go terminal UI for configuring, deploying and monitoring Stellar validators (stellar-core) over SSH. `SPEC.md` is the source of truth: read it before writing code. If the code needs to depart from it, stop and ask rather than improvise.

## How to work

- Build the milestones in `SPEC.md` ("Build order and acceptance criteria") strictly in order, M0 to M7. Finish each one with its tests passing before starting the next.
- Read-only features (SSH, dashboard) come before anything that writes to a host.
- When a decision in `SPEC.md` asks for verification (for example the `HIGH` three-validator count in M4), verify it and record the result in `docs/decisions.md`.
- Commit in small steps with clear messages and DCO sign-off (`git commit -s`).
- Check current library versions with `go list -m -versions <module>`; do not guess them.

## Stack

- Go 1.25+, no CGO, one static binary.
- `charm.land/bubbletea/v2`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, `charm.land/huh/v2` (v2 import paths, not `github.com/charmbracelet/...`).
- `golang.org/x/crypto/ssh` with `ssh/agent` and `ssh/knownhosts`; `github.com/kevinburke/ssh_config`; `github.com/pelletier/go-toml/v2`.

## Layout

```
cmd/auriga/            main, flags
internal/app/          root model, router, keymap
internal/ui/           screens and shared components
internal/sshx/         ssh config, auth, known_hosts, ProxyJump, pool, exec, dialer
internal/remote/       embedded helper scripts + typed wrappers
internal/corehttp/     typed admin-endpoint client
internal/stellarcfg/   config model, parse, render, validate, diff, redact
internal/quorum/       quorum builder and checks
internal/presets/      embedded testnet/pubnet presets
internal/inventory/    local inventory file
internal/audit/        audit log
internal/strkey/       Stellar key encoding and validation
testdata/              golden files and recorded JSON
test/integration/      Docker Compose end-to-end tests
```

## Commands

Create these Makefile targets in M0 and keep them working:

- `make build`
- `make lint` (gofmt check, `go vet`, `golangci-lint`)
- `make test` (`go test -race ./...`)
- `make test-secrets` (the secrets-leak test; run before every commit)
- `make itest` (Docker Compose integration suite)
- `make vuln` (`govulncheck ./...`)

## Hard rules

1. Secret seeds never leave the validator host, except an operator paste sent over stdin. Never write a secret to disk, logs, drafts, crash reports or the clipboard. Hold secrets in `[]byte` and zero them after use.
2. All remote work goes through the embedded helper scripts in `internal/remote`. No ad-hoc shell strings. Every argument is allowlist-validated and shell-quoted; secrets travel only on stdin, never in argv.
3. The admin HTTP client may call only `info`, `quorum`, `peers`, `upgrades`, `sorobaninfo` and `dumpproposedsettings`. Never add `tx`, bans, surveys, `manualclose` or load generation.
4. Never render `PUBLIC_HTTP_PORT=true`. Never add an option to disable host-key checking.
5. Bubble Tea `Update` never blocks or does I/O; all I/O goes through `tea.Cmd` and the per-host workers.
6. Every mutating action has a preview, a confirmation (typed on pubnet or when irreversible) and an audit-log entry.
7. Config rendering is deterministic. Update golden files only deliberately, never just to make a failing test pass.

## Testing

- Unit tests sit next to the code; golden files and recorded `info`/`quorum`/`peers` JSON live in `testdata/`.
- SSH tests use an in-process test server; unit tests never touch real hosts.
- Integration tests run on a private network passphrase behind a hidden test-only flag; never point tests at testnet or pubnet.

## Stellar specifics

- Hosts: config `/etc/stellar/stellar-core.cfg` (TOML), systemd unit `stellar-core`, system user `stellar`.
- Run the CLI as `sudo -u stellar stellar-core --conf <path> <command>`.
- Admin endpoint: `127.0.0.1:11626` by default, reached through an SSH direct-tcpip channel.
- Do not hard-code the upgrade expiry window; see the background section of `SPEC.md`.
- When unsure how stellar-core behaves, check `docs/software/commands.md` in the stellar-core repository and the SDF admin guide rather than guessing.
