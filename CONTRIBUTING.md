# Contributing to Auriga

Thanks for helping. Read `SPEC.md` before changing behaviour: it is the source
of truth, and a change that departs from it needs discussion first.

## Development setup

- Go 1.26 or later (no CGO).
- Docker with Compose v2 for the integration suite.
- Tools: `golangci-lint` v2 and `govulncheck`:

  ```sh
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
  go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
  ```

## Make targets

| Target | What it does |
| ------ | ------------ |
| `make build` | Static binary at `bin/auriga` (`CGO_ENABLED=0`, `-trimpath`) |
| `make lint` | gofmt check, `go vet`, `golangci-lint` |
| `make test` | `go test -race ./...`, including UI snapshot tests |
| `make test-secrets` | The secrets-leak suite. Run it before every commit. |
| `make itest` | Docker Compose integration suite on a private network passphrase |
| `make vuln` | `govulncheck ./...` |

UI snapshots and other golden files are updated only on purpose, never to make
a failing test pass:

```sh
go test ./internal/app -update   # then review the diff of testdata/
```

Commit golden-file updates on their own, with the reason in the message.

## Commits and sign-off

Every commit must carry a Developer Certificate of Origin sign-off
(<https://developercertificate.org/>):

```sh
git commit -s
```

Keep commits small, with a clear message.

## Rules that are never relaxed

- Secret seeds never leave the validator host except an operator paste sent
  over stdin, and never touch local disk, logs, drafts, crash reports or the
  clipboard.
- Remote work runs only through the embedded helper scripts in
  `internal/remote`; no ad-hoc shell strings.
- The admin HTTP client calls only `info`, `quorum`, `peers`, `upgrades`,
  `sorobaninfo` and `dumpproposedsettings`.
- Never render `PUBLIC_HTTP_PORT=true`, and never add a way to skip host-key
  checking.

## Adding a quorum preset

Presets live in `internal/presets` (from milestone M4). Each preset records
its source URL and snapshot date. Update one by replacing the embedded file
with the new upstream snapshot, updating the date, and including the upstream
diff in the pull request description.
