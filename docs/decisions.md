# Decisions

Implementation decisions that the spec leaves open, the answers to questions
raised while planning, and the results of verifications `SPEC.md` asks for.
The spec's own settled decisions are in its "Decisions" section and are not
repeated here.

## Answers to planning questions (14 Nov 2026)

The maintainer accepted the proposed defaults for all six questions.

| # | Question | Decision |
| - | -------- | -------- |
| Q1 | The rendered config header includes the UTC time, but rendering must be byte-identical for the same model. | The timestamp is an input to the renderer, like an injected clock. Golden tests fix it. Verify-staged compares against the exact bytes Auriga rendered. |
| Q2 | Huh input fields store values as `string`, which breaks the `[]byte`-only rule for secrets. | Seeds, sudo passwords and key passphrases use a custom masked-input component that writes into a `secret.Buffer`. Huh is used for every non-secret field. |
| Q3 | The M1 probe needs the redacted config read, which the build order lists under M3. | The read helper and on-host public-key derivation land in M1. Parsing and classification stay in M3. |
| Q4 | Checking `<archive-url>/.well-known/stellar-history.json` from the operator's machine would be a network call the security section forbids. | The check runs on the host with `curl`, through a helper script. |
| Q5 | Branching. | One branch and one pull request per milestone (`m0-scaffold`, `m1-ssh`, …), with small signed-off commits. |
| Q6 | Security contact and Homebrew tap. | Vulnerabilities are reported through GitHub private vulnerability reporting until a dedicated address exists. No tap until after v0.1. |

## Implementation decisions

| Date | Decision | Reason |
| ---- | -------- | ------ |
| 2026-11-14 | The `go` directive is 1.26.7, not 1.25. | `charm.land/bubbletea/v2` v2.1.1, `charm.land/lipgloss/v2` v2.1.0 and `ultraviolet` require Go 1.26. This still meets the spec's "Go 1.25 or later". |
| 2026-11-14 | `--config` names Auriga's config **directory** (inventory, settings, drafts, presets), not a single file. | The spec lists the flag without saying what it points at, and every local file lives in the config directory. |
| 2026-11-14 | Bubble Tea's own panic handler is disabled (`tea.WithoutCatchPanics`). The root model recovers panics in `Update`, `View` and commands, then writes a redacted crash report to the state directory. | Bubble Tea's handler prints the panic value and stack to the terminal unredacted, which conflicts with "no secret in crash output". |
| 2026-11-14 | Golden files live in each package's own `testdata/`. Fixtures shared by several packages (recorded `info`/`quorum`/`peers` JSON, sample configs) live in the top-level `testdata/`. | Go tooling resolves `testdata/` per package; the shared fixtures still sit where `CLAUDE.md` says. |
| 2026-11-14 | Two small packages were added beyond the layout in `CLAUDE.md`: `internal/xdg` (config and state paths, owner-only file writes) and `internal/secret` (redaction, and the secret buffer from M1). | Several packages (app, inventory, audit) need the same paths and redaction, and neither fits an existing package. |
| 2026-11-14 | `github.com/charmbracelet/x/exp/teatest/v2` is pinned to a pseudo-version. | The module has no tagged releases; it targets `charm.land/bubbletea/v2`. |
| 2026-11-14 | GitHub Actions run `actions/checkout@v7`, `actions/setup-go@v7` and `golangci-lint-action@v9` with golangci-lint v2.15.0. | Latest major tags as of this date; Dependabot keeps them current. |

## Repository settings (15 Nov 2026)

| Setting | Value | Reason |
| ------- | ----- | ------ |
| Pull request creation | Collaborators only | Outside contributors take part through issues. |
| `main` ruleset | No deletion or force push; pull request required with 0 approvals, stale approvals dismissed and conversations resolved; `Lint, test, secrets, vuln` must pass on an up-to-date branch; repository admins may bypass for pull requests only | One maintainer cannot approve their own pull request, so required approvals would block every merge. |
| Merge methods | Merge commit and rebase; squash off | `CLAUDE.md` asks for small commits, which squash merging would collapse. |
| `release-tags` ruleset | `v*` tags cannot be created, moved or deleted except by repository admins | Releases are built and signed from these tags. |
| Actions | GitHub-owned actions plus `golangci/golangci-lint-action` only; default token read-only; workflows from every outside contributor need approval | Supply-chain hardening (`SPEC.md` "Supply chain"). |
| Security | Private vulnerability reporting, Dependabot alerts and security updates, secret scanning with push protection | `SECURITY.md` points to private reporting. |
| Web commits | Sign-off required | Matches the DCO requirement. |
| DCO | The DCO GitHub App checks every pull request, and its `DCO` check is required on `main` | Enforces `git commit -s` on pushed commits as well as web commits. |
| Not enforced | Signed (GPG or SSH) commits; actions pinned to commit SHAs | Not used yet; either can be turned on later. |

## Verifications

| Item | Milestone | Result |
| ---- | --------- | ------ |
| How stellar-core counts the three-validator minimum for a `HIGH` org when a node lists only its siblings | M4 | Pending |
| The helper-script stdin protocol (script, sudo password and seed on one stdin) works under dash and bash | M1 | Pending |
| `ssh -G` output across OpenSSH versions | M1 | Pending |
| Package-default config fingerprint from `apt.stellar.org` | M3 | Pending |
| `expirationminutes` acceptance by stellar-core build | M6 | Pending |
