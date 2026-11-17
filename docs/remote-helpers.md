# Remote helper reference

Everything Auriga does on a host is one of the shell scripts embedded from
[`internal/remote/scripts`](../internal/remote/scripts). There is no
free-form remote shell (SPEC.md "Running commands on a host"). This page
lists each helper and what it reads, writes and prints, so an operator can
audit what Auriga will run before giving it sudo.

## How a helper runs

1. Auriga opens one SSH `exec` channel and runs one fixed command:

   ```sh
   sudo -n -- sh -s -- 'ARG1' 'ARG2' …        # with a NOPASSWD sudoers rule
   sudo -k -S -p '' -- sh -s -- 'ARG1' …      # when sudo needs a password
   ```

2. The script goes in over stdin, wrapped so that `sh` parses all of it
   before it runs anything. With a password, the password line comes first
   and is read by sudo.
3. Arguments are checked against a per-kind allowlist (below), then
   single-quoted. A value that fails its allowlist is refused before the
   SSH channel opens.
4. Secrets are never arguments. A helper that needs one (only `stage` in
   `import` mode) prints `@@ready-secret`, and Auriga writes the secret to
   stdin only once it sees that line.
5. stellar-core always runs as `stellar` (`sudo -n -u stellar -- stellar-core
   …`) from the unit's working directory, so relative paths in the config
   resolve as they do for the running node.

Progress lines start with `@@progress`. Results are `key=value` lines on
stdout. On failure, Auriga shows the helper's name, exit code and the last
20 lines of stderr, with anything shaped like a secret seed redacted.

### Argument allowlists

| Kind | Allowed | Example |
| ---- | ------- | ------- |
| alias | letters, digits, `._-`, up to 63 | `acme-a` |
| name (service, archive) | letters, digits, `._@-`, up to 63 | `stellar-core` |
| path | absolute, `A-Za-z0-9._/+-`, no `.` or `..` segments, up to 1023 | `/etc/stellar/stellar-core.cfg` |
| port | 1–65535 | `11626` |
| word | a fixed keyword chosen by Auriga | `keep`, `new-db` |
| url | `http(s)://host[:port][/path]`, no query | `https://history.acme.org/.well-known/stellar-history.json` |
| base64 | standard base64, up to 120,000 bytes | a rendered config (its `NODE_SEED` is a marker) |
| admin | one of the six admin commands, URL-encoded query | `upgrades?mode=get` |
| admin (raw) | the same, query decoded; no `%`, and no decoded value holding `&`, `?` or `#` | `upgrades?mode=set&upgradetime=2026-11-23T17:00:00Z&basefee=200` |

The allowlists and the quoting are fuzzed (`FuzzArgValidate`,
`FuzzRawCommand`, `FuzzQuote`).

## Read-only helpers

| Helper | Arguments | Does | Prints |
| ------ | --------- | ---- | ------ |
| `probe` | config path, service | Describes the host: hostname, OS, NTP sync, the `stellar` user, stellar-core version, the unit's state and working directory, the config's presence, owner, mode and sha256, its `HTTP_PORT` and `BUCKET_DIR_PATH`, and free disk. The config itself is not printed. | `key=value` facts |
| `read-config` | config path | Prints the config with the `NODE_SEED` value replaced by `<redacted:G…>`. The public key is derived on the host with `stellar-core sec-to-pub`, and the seed goes to it on stdin. | `pubkey=`, `seed=`, `node_alias=`, `---`, then the redacted file. Exit 3: no config; 4: unreadable |
| `list-backups` | config path | Lists `<config>.auriga-<UTC>.bak` files. | `backup=<path>` lines with mtime, size, owner, mode |
| `check-archive` | archive URL, program names | As `stellar`, checks that the archive commands' programs exist, and whether the archive's `.well-known/stellar-history.json` answers. | `bin=<name>:<yes\|no>`, `exists=<yes\|no\|unknown>` |
| `admin-get` | port, command, raw command, config path, service | Fallback for the admin endpoint when sshd refuses port forwarding. Uses `curl` against `127.0.0.1:<port>`, or `stellar-core http-command` (which does not URL-decode, hence the raw form). | The response body, then `@@status=<code> via=<curl\|http-command>` |

`admin-get` carries `upgrades?mode=set|clear`, which change a node's vote.
Auriga sends those only from the upgrades screen, after the preview and
confirmation, and writes an audit entry for each.

## Helpers that change a host

Each runs only after the deploy or restore review has been confirmed, and
each step writes an audit-log entry.

| Helper | Arguments | Does | Prints |
| ------ | --------- | ---- | ------ |
| `preflight` | config path, service | Checks that the unit exists, there is free space where the config and the node's data live, and there are no leftovers from an interrupted deploy. Changes nothing. | `key=value` checks |
| `gen-seed` | config path | Runs `stellar-core gen-seed` and keeps the seed in a root-only pending file, `<config>.auriga-pending-seed` (mode `0600`). Running it again reuses that seed. | `pubkey=`, nothing else |
| `stage` | config path, mode (`keep`, `generate`, `import`, `none`), rendered config (base64) | Writes `<config>.auriga-staged`, `root:stellar` mode `0640`, with the seed marker replaced by the real seed. The seed comes from the current config, the pending file or stdin. It is substituted with shell built-ins, never passed to another program's arguments. | The staged file's sha256 and the seed's public key |
| `stage-backup` | config path, backup path | For a manual rollback: copies a backup, seed and all, to `<config>.auriga-staged`. | The staged file's sha256 |
| `backup` | config path | Copies the config to `<config>.auriga-<UTC>.bak` with the same owner and mode, and keeps the ten newest. | `backup=<path>`, empty if there was no config |
| `install` | config path | Renames the staged file over the config (atomic, same file system) and removes the pending seed. | `sha256=` of the installed file |
| `init` | config path, service, `new-db` or `new-hist`, archive name | Stops the service and runs the one-off initialisation. A rollback cannot undo either, so the review asks for typed confirmation on a running node. | — |
| `restart` | service | `systemctl restart` (or start). | `active=<state>` |
| `rollback` | config path, service, backup path (optional) | Restores the backup over the config (copy, then atomic rename) and restarts. Without a backup (a new node), moves the failed config aside and stops the service. | `active=<state>` |

## Files Auriga creates on a host

All of them sit next to the config:

| File | Mode | Lifetime |
| ---- | ---- | -------- |
| `<config>.auriga-staged` | `root:stellar 0640` | From stage until install or the next deploy |
| `<config>.auriga-pending-seed` | root, `0600` | From gen-seed until install |
| `<config>.auriga-<UTC>.bak` | Same as the config | The ten newest are kept |
| `<config>.auriga-failed-<UTC>` | Same as the config | A new node's config that failed its health gate |

`preflight` reports leftover staged or pending files from an interrupted
deploy.

## The sudoers rule

The helpers need root, both to write the config and to run stellar-core as
`stellar`. Because each helper is a script fed to `sh` on stdin, sudo cannot
narrow them one by one: the rule grants `sh` as root. Choose one:

- **A password each session.** Auriga asks once per host per session, keeps
  the password in memory only, and zeroes it on exit.
- **A NOPASSWD rule for a dedicated deploy user.** This is the README's
  recommendation, for a user that has no other use and logs in only with
  your SSH key:

  ```
  # /etc/sudoers.d/auriga  (edit with: visudo -f /etc/sudoers.d/auriga)
  deploy ALL=(root) NOPASSWD: /bin/sh -s --*, /usr/bin/sh -s --*
  ```

  The rule still lets that user run any script as root. Its protection is
  that the account is reachable only through your key. The Docker
  integration suite runs its passwordless hosts with exactly this rule.
