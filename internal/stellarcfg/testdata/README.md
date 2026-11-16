# stellarcfg test data

`input/` holds configs as Auriga receives them: read on the host with
`NODE_SEED` redacted to `<redacted:G…>`.

- `sdf-pubnet-validator-full.cfg`: SDF's example full validator config, copied
  unchanged from stellar/packages (Apache-2.0),
  `docs/examples/pubnet-validator-full/stellar-core.cfg` at commit
  `e3a92b9d74a0a2f2071923516d238dceca60df33` (2026-08-25). Its `NODE_SEED`
  is commented out.
- `package-testnet-validator.redacted.cfg`: `/etc/stellar/stellar-core_testnet_validator.cfg`
  as installed by the stellar-core 30.0.0 package from apt.stellar.org, with
  its published example seed redacted the way the read-config helper does.
- `itest-validator.redacted.cfg`: host-a's config from the integration stack
  (test vector seed, redacted).
- `adopted-unknown-keys.cfg`, `legacy-quorum-set.cfg`, `malformed.cfg`,
  `plain-seed.cfg`: hand-written cases.

`render/` holds the golden renders. Update them only on purpose:
`go test ./internal/stellarcfg -update`, then review the diff.
