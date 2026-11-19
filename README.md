# Auriga

Auriga is an open-source terminal UI for configuring, deploying and monitoring
Stellar validators (stellar-core) over SSH. It is usually used for an
organisation's three validators, from one screen.

![Auriga: adding three hosts, the live dashboard, a host's quorum and the upgrades screen](docs/demo.gif)

> **Status:** release candidates for v1.0.0. The features below work and are tested against
> stellar-core 30 in a Docker suite. Run the
> [acceptance checklist](docs/acceptance.md) on testnet before trusting it
> with a pubnet validator.

## What it does

- **Connects** to the hosts already in your `~/.ssh/config`, including
  through `ProxyJump`, with your existing keys or agent. Host keys are always
  checked.
- **Adopts** running nodes without changing them. It reads each config with
  the seed redacted on the host and checks it against stellar-core's rules.
- **Monitors** every node's `info` live, every 5 s, over SSH. It flags lost
  quorum intersection, nodes falling behind, version mismatches and
  differing upgrade votes.
- **Sets up an org end to end** with an eight-step wizard: network, keys
  (generated on the host, kept, or pasted), node type, quorum set with SDF's
  presets, history archive (S3-compatible, local with nginx, or custom) and
  advanced settings.
- **Deploys** one host at a time. Each host is staged, verified against
  what you reviewed, backed up and installed, then held behind a health
  gate and a sync gate. A node that does not come back is rolled back
  automatically. Any backup can be restored by hand later.
- **Arms network upgrades** from a form or a pasted SDF arming string. Each
  host is checked against what it supports. It can also clear a vote and
  show what a Soroban settings proposal would change.
- **Logs** every change in a local audit log.

## Install

Download the archive for your platform from the
[releases page](https://github.com/owenjacob-dev/auriga/releases). Linux and
macOS builds exist for amd64 and arm64. Then verify the archive and
install:

```sh
# Check the archive against the signed checksums (needs cosign v3).
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/owenjacob-dev/auriga/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
sha256sum --ignore-missing -c checksums.txt     # macOS: shasum -a 256 -c

tar xzf auriga_*_linux_amd64.tar.gz
install -m 755 auriga ~/.local/bin/
```

Each archive has an SPDX SBOM next to it (`*.sbom.spdx.json`).

Or build from source, with Go 1.26 or later:

```sh
make build            # static binary at bin/auriga
./bin/auriga --version
```

## Quick start

1. **Give each validator an alias in `~/.ssh/config`.** If you can
   `ssh acme-a`, Auriga can connect to it too.

   ```
   Host acme-a
     HostName core-a.acme.org
     User deploy
   Host acme-c
     HostName 10.0.2.15
     User deploy
     ProxyJump bastion.acme.org
   ```

2. **Set up sudo on each host.** Auriga's helpers need root, to write the
   config and to run `stellar-core` as `stellar`. Either let Auriga ask for
   the sudo password once per session, or (recommended) give a dedicated
   deploy user this rule:

   ```
   # /etc/sudoers.d/auriga   (edit with: visudo -f /etc/sudoers.d/auriga)
   deploy ALL=(root) NOPASSWD: /bin/sh -s --*, /usr/bin/sh -s --*
   ```

   Auriga feeds its embedded helper scripts to `sh` on stdin, so the rule
   grants a root shell. Keep the account reachable only with your SSH key.
   [docs/remote-helpers.md](docs/remote-helpers.md) lists every script
   Auriga runs.

3. **Run `auriga`, press `a` and pick your hosts.** Auriga connects to each
   one, asks you to confirm any unknown host key, and probes it (reads
   only). Running nodes show up on the dashboard with their live `info`.

4. **Press `s` to set up or adopt an org.** The wizard ends with a per-host
   diff. Press `D` on the review to deploy.

5. **Press `u` for network upgrades and `l` for the audit log.** Press `?`
   for the keys on any screen.

| Key | Where | Action |
| --- | ----- | ------ |
| `a` | Dashboard | Add hosts from `~/.ssh/config` |
| `s` | Dashboard | Setup wizard for the selected host's org |
| `u` | Dashboard | Network upgrades |
| `l` | Dashboard | Audit log |
| `enter` | Dashboard | Host detail: Info, Quorum, Peers, Upgrades, Config, Probe |
| `r` | Dashboard, detail | Refresh now, or reconnect |
| `p` | Dashboard | Pause or resume polling |
| `x` | Dashboard | Remove a host from the inventory (local only) |
| `R` | Config tab | Restore a backup |
| `?` / `esc` / `q` | Anywhere | Help / back / quit |

Auriga keeps its state in `~/.config/auriga` (inventory, settings, wizard
drafts) and `~/.local/state/auriga` (audit log, and a debug log with
`--debug`), with owner-only permissions. None of these files ever holds a
secret.

## Security model in brief

- Secret seeds stay on the validator host. Reads are redacted there, new
  seeds are generated there, and kept seeds are copied there. A seed
  crosses SSH only when you paste one in, and then only over stdin. Auriga
  never writes a secret to your disk, logs, drafts, crash reports or
  clipboard, and a test suite checks this on every change.
- Only embedded helper scripts run on hosts. Every argument is
  allowlist-checked and quoted, and secrets are never arguments.
- Every change shows a preview and asks for confirmation. Pubnet changes and
  steps that can't be undone need a typed confirmation.
- The stellar-core admin port is reached only through SSH. Only six
  read-mostly admin commands can be called, and `PUBLIC_HTTP_PORT=true` is
  never written.
- Host keys are always verified. There is no option to skip that.
- No telemetry and no update checks.

Details are in [docs/threat-model.md](docs/threat-model.md). To report a
vulnerability, see [SECURITY.md](SECURITY.md).

## Documentation

- [SPEC.md](SPEC.md): the design, and the source of truth
- [docs/architecture.md](docs/architecture.md): how the code fits together
- [docs/remote-helpers.md](docs/remote-helpers.md): every script Auriga runs on a host
- [docs/threat-model.md](docs/threat-model.md): what Auriga protects and how
- [docs/decisions.md](docs/decisions.md): behaviour verified against stellar-core
- [docs/acceptance.md](docs/acceptance.md): the manual release checklist

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Auriga is licensed under
[Apache-2.0](LICENSE). It is not affiliated with the Stellar Development
Foundation.
