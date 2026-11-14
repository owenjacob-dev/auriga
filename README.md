# Auriga

Auriga is an open-source terminal UI for configuring, deploying and monitoring
Stellar validators (stellar-core) over SSH, usually an organisation's three
validators from one screen.

> **Status:** early development. The app currently shows an empty dashboard;
> SSH connections, monitoring, the setup wizard, deploys and network upgrade
> votes arrive in the milestones listed in [`SPEC.md`](SPEC.md).

## What it will do (v1)

- Connect to hosts already defined in `~/.ssh/config`, with your existing SSH
  keys or agent.
- Configure a node end to end: network, keys, validator type, quorum set and
  history archive.
- Adopt nodes that are already running without breaking them.
- Show each node's `info` endpoint live, fetched over SSH.
- Inspect, arm and clear network upgrade votes across hosts.

## Build

```sh
make build
./bin/auriga --version
```

Requires Go 1.26 or later. The binary is static (no CGO) and runs on Linux and
macOS.

## Security model in brief

- Secret seeds stay on the validator host; Auriga never writes one to your
  disk, logs, drafts, crash reports or clipboard.
- Every change shows a preview and asks for confirmation; pubnet changes need
  a typed confirmation.
- The stellar-core admin port is reached only through SSH, never exposed.
- Host keys are always verified.

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Auriga is licensed under
[Apache-2.0](LICENSE).
