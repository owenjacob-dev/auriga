# stellar-core admin endpoint fixtures

Shared by `internal/corehttp`, `internal/health` and the UI tests.

- `recorded/<build>/` holds responses captured from real stellar-core
  builds (the integration stack, and older builds for decoding tests). Each
  directory says which build produced it. These are the source of truth for
  field names.
- `synthetic/` holds hand-written responses in the documented format, for
  states that are hard to produce on demand (a lost quorum intersection, a
  node far behind). Each file states what it represents. Keep them in line
  with the recorded ones; never invent fields that real builds do not send.

Synthetic files are named for what they show. `quorum-tiered-named.json` is
an org's validator in a five-org tiered quorum set, with nodes reported by
the names in its config (as `recorded/` shows real builds do), one inner set
nested a level deeper, and one node given only by its key.
