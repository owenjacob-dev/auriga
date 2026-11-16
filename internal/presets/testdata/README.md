# Upstream sources of the embedded presets

- `upstream/pubnet-validator-full.cfg`: stellar/packages (Apache-2.0),
  `docs/examples/pubnet-validator-full/stellar-core.cfg` at commit
  `e3a92b9d74a0a2f2071923516d238dceca60df33` (2026-08-25), unchanged.
- `upstream/stellar-core_testnet_validator.cfg`: stellar/stellar-core
  (Apache-2.0), `docs/stellar-core_testnet_validator.cfg` at commit
  `eaf955825a8e2480e9cc979c6486d3a2da43d524` (2024-12-13), with its published
  example seed replaced by a placeholder. Identical to the file the
  stellar-core 30.0.0 package installs.

`data/*.toml` is generated from these with the same `Extract` the refresh
action uses: `go test ./internal/presets -update`.
