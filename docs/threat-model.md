# Threat model

Auriga runs on an operator's laptop and drives Stellar validators over SSH.
This page covers what it protects, who it protects against, and where each
defence is tested. The requirements come from SPEC.md "Security
requirements".

## What is protected

| Asset | Why it matters |
| ----- | -------------- |
| Validator secret seeds (`NODE_SEED`) | Sign consensus messages for the org. A leaked seed lets anyone impersonate the validator. |
| Sudo passwords, SSH key passphrases | Root on the validator hosts. |
| Object-storage credentials for archives | Write access to the org's public history. |
| The validator's vote and config | A wrong quorum set or an armed upgrade changes how the network reaches consensus. |
| Host identity | Connecting to the wrong host would send it configs and pasted seeds. |

## Trust boundaries

```
 operator ──keys──▶ Auriga (laptop) ──SSH──▶ [jump host] ──SSH──▶ validator host
                      │                                            │
             local files: inventory,                     root via sudo; stellar-core
             drafts, audit, debug log                    admin port on 127.0.0.1
```

- **The laptop** is trusted while Auriga runs. Its files, logs, screen and
  clipboard are not: they get backed up, shared and screen-recorded.
- **Validator hosts** are trusted with their own seeds, and that is all.
  Output from a host (helper output, admin JSON, configs) is treated as
  untrusted input.
- **Jump hosts** only forward TCP. End-to-end host-key checking covers the
  validator behind them.
- **The network** between the two is hostile.

## Threats and defences

### A seed reaches the laptop

| Path | Defence | Test |
| ---- | ------- | ---- |
| Reading a config | Redacted on the host (`read-config`). The public key is derived there and the seed never crosses SSH. | `TestReadConfigRedactsOnHost`, `TestSSHFlowDoesNotLeak` |
| Generating a key | `gen-seed` keeps the seed in a root-only pending file on the host. Only the public key comes back. | `TestDeployGenerateDoesNotLeak` |
| Keeping a key | `stage` copies it host-side from the current config. | `TestStageSeedSources` |
| Importing a key | The operator's paste is held in a `[]byte` buffer, sent on stdin only after the helper asks for it, then zeroed. Never argv, never disk. | `TestWizardImportDoesNotLeak`, `TestDeployImportDoesNotLeak` |
| Manual rollback | `stage-backup` copies the backup host-side. | `TestManualRestoreOnRealHost` (integration) |
| Logs, crash reports, drafts | Drafts store no secrets. The debug log and crash report go through a redacting handler, as defence in depth. | `TestCrashPathDoesNotLeak`, `FuzzRedact` |
| The screen | Seeds are shown as `S…` plus the public key, and passwords as dots. | UI snapshot tests |
| The clipboard | Only the Info, Quorum, Peers and Upgrades tabs' admin JSON can be copied (`c`), never a config or a secret. | `TestDetailCopyJSON` |

Each leak test plants known fake secrets, runs the real code path (TUI,
SSH server, host helpers, `--debug` log, audit log), then greps every file
under the XDG config, state and home directories, plus all terminal
output (`make test-secrets`, run on every pull request).

### A secret shows up in a process list or shell history on the host

Arguments carry no secrets. The seed substitution in `stage` uses shell
built-ins, so the seed never appears in another process's argv. The shims
in the tests log every `stellar-core` argv, and the tests assert the seed
is absent.

### Command injection through a host, an inventory file or a paste

- Only embedded helper scripts run, fed to `sh -s` on stdin. There is no
  free-form remote shell.
- Every argument matches a per-kind allowlist and is single-quoted.
  `FuzzArgValidate`, `FuzzQuote` and `FuzzRawCommand` cover the allowlists,
  the quoting and the decoded admin query on the fallback transport.
- A pasted arming string is parsed into typed values (a time, uint32s, a
  64-byte base64 key), not passed through (`FuzzParse`).
- A host's output is parsed as `key=value` lines or JSON, never evaluated
  (`FuzzDecode`, `FuzzEvaluate`).

### Man in the middle, or the wrong host

- Host keys are checked against `~/.ssh/known_hosts`, including on every
  hop of a `ProxyJump`. An unknown key needs explicit trust. A changed key
  blocks and shows both fingerprints. There is no option to turn this off
  (CLAUDE.md hard rule 4).
- The admin endpoint is reached through an SSH `direct-tcpip` channel to
  `127.0.0.1`. `PUBLIC_HTTP_PORT=true` is never rendered, and is flagged red
  on adopted hosts.

### An operator mistake breaks consensus

- Every change has a preview (a per-host diff or the exact upgrade
  request) and a confirmation. Pubnet changes and steps that can't be
  undone (`new-db`, `new-hist` on a running node, replacing a running key)
  need typed confirmation.
- Quorum checks mirror stellar-core's own rules: 14 recorded fixtures are
  verified against core 30. A config core would refuse is blocked before
  it is staged.
- Deploys go one host at a time behind health and sync gates, and roll back
  automatically if the node does not come back.
- Upgrade arming checks each host's supported protocol and flags hosts in
  one org that are armed differently.
- Every mutating step writes an audit-log entry with the config hash and
  backup path.

### The admin client is turned against the node

The client can call only `info`, `quorum`, `peers`, `upgrades`,
`sorobaninfo` and `dumpproposedsettings`. It cannot send transactions,
bans, surveys, `manualclose` or load generation. Both the HTTP client and
the fallback helper check this (`TestCommandsAreTheSixAllowed`,
`TestAdminAllowlistOnHost`).

### Supply chain

- Dependencies are pinned in `go.sum`. `govulncheck` runs on every pull
  request, and Dependabot proposes updates.
- Release binaries are built with `-trimpath` and commit timestamps, so
  they are reproducible. They are published with SHA-256 checksums, an
  SPDX SBOM per archive and a Sigstore keyless signature over the checksums.
  The release job runs only after lint, tests, fuzzing and the Docker
  integration suite pass on the tag.
- Auriga makes no network calls except SSH to inventory hosts. It has no
  telemetry or update checks.

## Out of scope

- **A compromised laptop while Auriga runs.** An attacker with your user's
  access can read process memory, your SSH agent and your keystrokes,
  including a seed as you paste it. Zeroing buffers shortens how long a
  secret sits in memory, but cannot stop this.
- **A compromised validator host.** It already holds its own seed. Auriga
  does not send a host any other host's seed.
- **The sudoers rule grants root `sh`.** See
  [remote-helpers.md](remote-helpers.md#the-sudoers-rule). The account it
  is granted to should be reachable only with your SSH key.
- **Denial of service by a host.** A host that hangs stalls its own row and
  nothing else: each host has its own worker and timeouts, and the UI never
  blocks on I/O.
