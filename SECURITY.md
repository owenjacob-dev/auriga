# Security policy

Auriga handles access to Stellar validators, whose secret seeds sign consensus
messages. Security reports are welcome and taken seriously.

## Reporting a vulnerability

Report privately through GitHub's private vulnerability reporting:
<https://github.com/owenjacob-dev/auriga/security/advisories/new>

Do not open a public issue, and do not include real secret seeds, passwords or
private keys in a report.

You can expect an acknowledgement within 7 days. We follow a 90-day disclosure
window: we aim to release a fix and publish an advisory within 90 days of the
report, and we will agree a disclosure date with you.

## Supported versions

Auriga is pre-1.0. Only the latest release receives security fixes.

| Version | Supported |
| ------- | --------- |
| latest `v0.x` | Yes |
| older | No |

## Security model in brief

- Secret seeds stay on the validator host. They cross the SSH connection only
  when the operator pastes one in, and then only over stdin.
- No secret is written to the operator's disk, logs, drafts, crash reports or
  clipboard.
- Host keys are always checked; there is no option to disable checking.
- The stellar-core admin HTTP port is reached only through the SSH connection
  and is never exposed.

The full requirements are in `SPEC.md` under "Security requirements".
