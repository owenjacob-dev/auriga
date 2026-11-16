# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Deploys (`D` on the wizard's review): per host, preflight, stage with the
  seed substituted on the host (kept, generated there, or pasted and sent
  over stdin), verify the staged file against the review, back up, install
  atomically, one-off `new-db`/`new-hist`, restart, a 60 s health gate that
  restores the backup automatically, the sync gate and a quorum check. Hosts
  deploy one at a time; all at once on testnet or for new nodes. Typed
  confirmation on pubnet and for init steps on existing nodes.
- Manual rollback (`R` on the Config tab): compare a backup with the running
  config, then restore it through the same steps and gates.
- Audit log (`l` on the dashboard): every step that changed a host, newest
  first, filterable by host or action, in `~/.local/state/auriga/audit.log`.
- The quorum checks now also follow stellar-core's rule that validators with
  an archive must be in every quorum, with the same consent for
  `UNSAFE_QUORUM`.
- The wizard's history step starts from the archive an adopted node already
  publishes to, and never initialises that archive again.
- Setup wizard (`s`): eight steps (scope, network, node type, keys, quorum,
  history archive, advanced, review) that configure an org's hosts at once.
  New drafts start from what adopted hosts already run, and every step saves
  a draft without secrets. Nothing touches a host before the review. A host
  can trust its own set of orgs in the Advanced step; by default all hosts
  share one.
- Quorum presets: SDF's testnet validators and the Tier 1 orgs for pubnet,
  embedded with their snapshot date; `ctrl+r` fetches the current list and
  shows a diff before replacing it.
- Live quorum preview and checks that match how stellar-core 30 builds and
  validates quorum sets, including consent before writing `UNSAFE_QUORUM`.
- History archive backends: local directory + nginx, S3-compatible (S3, R2,
  B2) and custom commands, checked on the host before deploying.
- Config model: each host's config (read with the seed redacted on the host)
  is classified as unconfigured, adoptable or read-only, and validated. Keys
  Auriga does not manage are kept verbatim; renders are deterministic and
  never contain a seed.
- Config tab: classification, problems, backups and the file, with `d` to show
  what a first deploy would change (adoption notes and a diff).
- `PUBLIC_HTTP_PORT=true` on any host is flagged in red.
- Live dashboard: polls each host's `info` every 5 s (configurable 2–60 s in
  `settings.toml`, `p` to pause) and shows state, ledger, age, peers, quorum,
  intersection, protocol and armed upgrades, coloured by SDF's monitoring
  thresholds. Stale values grey out; a lost quorum intersection raises a red
  banner. Every problem is also written out in the status line.
- Host detail tabs: Info, Quorum (with an on-demand transitive check), Peers,
  Upgrades, Config (redacted) and Probe. `c` copies a tab's JSON.
- The admin endpoint is reached through the SSH connection. When sshd refuses
  port forwarding, Auriga runs `curl` (or `stellar-core http-command`) on the
  host instead and says which it uses.
- Host picker (`a`): choose aliases from `~/.ssh/config` (following
  `Include`), with filter and multi-select. Aliases resolve with `ssh -G`, or
  with a built-in parser and a warning when OpenSSH is missing.
- SSH connections with agent keys, key files (including encrypted keys and
  `-cert.pub` certificates) and `ProxyJump` chains. Unknown host keys ask
  first (reject, trust for the session, or trust and save); changed keys are
  refused and show both fingerprints.
- One connection per host with keepalive, automatic reconnect with backoff,
  and at most four channels per host.
- Host probe: stellar-core version, service state, config ownership and mode,
  public key derived on the host (the seed is never read off the host), free
  disk space and clock sync. sudo works with or without a password; a
  password is asked once per host per session.
- Inventory file (`~/.config/auriga/inventory.toml`); hosts that share a home
  domain are grouped into one organisation.
- `--debug` writes a redacted JSON log to the state directory.
- Project scaffold: Makefile targets, CI, lint configuration and community files.
- Empty terminal UI with the dashboard frame, help overlay (`?`), quit
  confirmation while work is running, light/dark themes and `NO_COLOR` support.
- Redacted crash reports written to the state directory.
- Secrets-leak test harness (`make test-secrets`).
