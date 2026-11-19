# Contributing to Auriga

Thanks for your interest in Auriga.

## How to contribute

**Open an issue.** Bug reports, feature ideas and questions are all welcome as
GitHub issues. Pull requests can only be opened by repository collaborators,
so please do not prepare a pull request unless a maintainer has invited you
to. Report security problems privately as described in `SECURITY.md`, never in
a public issue.

The rest of this page is for collaborators. Read `SPEC.md` before changing
behaviour: it is the source of truth, and a change that departs from it needs
discussion first.

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
| `make fuzz` | Every fuzz target for `FUZZTIME` each (default 10s) |

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

## Merging

`main` is protected by a ruleset. Changes land through a pull request whose
`Lint, test, secrets, vuln` and `DCO` checks pass on a branch that is up to date with
`main`, with every review conversation resolved. Merge with **Rebase** or a
**merge commit**; squash merging is turned off so that small commits survive.
Release tags (`v*`) can only be created, moved or deleted by repository
admins.

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

## Fuzzing

Parsers that read input from a host, a file or a paste have fuzz targets
(`func Fuzz…`). CI runs each for 10 seconds on every pull request. Run one for
longer when you change it:

```sh
go test -run '^$' -fuzz '^FuzzParse$' -fuzztime 5m ./internal/upgrades
```

A failing input is saved under the package's `testdata/fuzz/`. Commit it with
the fix, so it stays a regression case.

## Adding a quorum preset

Presets live in `internal/presets/data`, one TOML file per network. Each
records its `source` URL and `snapshot` date. To update one:

1. Replace the file with the new upstream snapshot and update the date.
2. Run `go test ./internal/presets ./internal/ui/wizard`.
3. Put the upstream diff in the pull request description.

## Releasing

Releases are cut by an admin from `main`, release candidate first:

1. Tag a release candidate on `main` and push it:
   `git tag -a vX.Y.Z-rc.N -m vX.Y.Z-rc.N && git push origin vX.Y.Z-rc.N`.
2. The CI release job runs only after lint, tests, fuzzing and the
   integration suite pass on the tag. It creates a draft GitHub release with
   the archives, checksums, SBOMs and Sigstore bundle, marked as a
   pre-release. A release candidate takes its notes from `[Unreleased]` in
   `CHANGELOG.md`.
3. Run the manual acceptance in [`docs/acceptance.md`](docs/acceptance.md) on
   three testnet VMs with the release candidate's archives. Fix anything it
   finds and cut `rc.N+1`.
4. Move the `[Unreleased]` entries in `CHANGELOG.md` under a new
   `## [X.Y.Z] - YYYY-MM-DD` heading, attach the filled-in results table to
   that pull request, and merge it.
5. Tag `vX.Y.Z` on the merge commit and push it. The release job creates the
   draft release. Check it, then publish it. Delete the release candidates'
   drafts, or publish them as pre-releases.
