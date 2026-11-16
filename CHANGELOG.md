# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

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
