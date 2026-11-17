# Manual acceptance before a release

SPEC.md requires a manual run before each release, on three cloud VMs on
**testnet**. Run it with the release candidate binary
(`make build VERSION=v0.1.0-rc.N`, or a snapshot from
`goreleaser release --snapshot --clean --skip=sign`). Record the results at
the bottom, and attach the filled-in table to the release pull request.

Never run this against pubnet validators. Testnet is reset periodically, so
the seeds generated here protect nothing.

## Setup

- Three Ubuntu 24.04 VMs (2 vCPU, 8 GB RAM, 100 GB SSD is enough for
  testnet), each with `stellar-core` 30 from `apt.stellar.org` and the
  packaged `stellar-core` unit. Leave them unconfigured: the wizard writes
  the configs.
- A fourth small VM as a bastion, or use one of the three as the jump host
  for the third.
- `nginx` on each VM, serving a directory such as `/var/www/history`, for the
  local archives.
- A PostgreSQL connection string per host, or SQLite paths if you accept
  SQLite for the test.
- A deploy user on each VM with the sudoers rule from the README, and your
  SSH key in its `authorized_keys`. Load the key into `ssh-agent`.
- `~/.ssh/config` on the laptop:

  ```
  Host acc-a
    HostName <ip-a>
    User deploy
  Host acc-b
    HostName <ip-b>
    User deploy
  Host acc-c
    HostName <private-ip-c>
    User deploy
    ProxyJump <bastion>
  ```

- Start with an empty state: `export XDG_CONFIG_HOME=$(mktemp -d)
  XDG_STATE_HOME=$(mktemp -d)`. Then run `auriga --debug`, so the debug log
  is covered by the seed search too.

## Checklist

Each item matches a line of SPEC.md "Acceptance checklist for v0.1.0".

1. **Three hosts at once, one behind ProxyJump, with agent keys.** Press `a`,
   select `acc-a`, `acc-b` and `acc-c`, and press enter. All three reach
   "connected" and show probe results. `acc-c` connects through the bastion.
2. **Host keys.**
   - Remove `acc-a` from `~/.ssh/known_hosts` and reconnect (`r`). Auriga
     prompts with the SHA256 fingerprint. Compare it with
     `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the VM.
   - Then put a wrong key for `acc-a` in `known_hosts` and reconnect. The
     connection is blocked, and both fingerprints are shown. Restore the
     right key.
3. **Live dashboard.**
   - Once the nodes run (after item 6), `info` refreshes every 5 s: the
     clock and ledger numbers move.
   - Stop the node on one host (`sudo systemctl stop stellar-core`). Its
     values grey out as stale after three polls.
4. **sshd stops and comes back.**
   - On `acc-b`, run `sudo systemctl stop ssh` while the dashboard is open.
     The UI stays responsive: move between rows, open help.
   - Run `sudo systemctl start ssh`. `acc-b` reconnects by itself, with no
     key press.
5. **Adopting a running validator changes nothing.** Use a running testnet
   validator you control, or `acc-a` after item 6.
   - Note `sudo sha256sum /etc/stellar/stellar-core.cfg` and
     `sudo ls -la --time-style=full-iso /etc/stellar/`.
   - Add the host and open its Config tab: `NODE_SEED` shows as
     `<redacted:G…>`.
   - Run both commands again. They print the same output.

   (The checklist says a pubnet validator. Only do that with one you
   operate, and only after this run has passed on testnet.)
6. **The wizard sets up a three-host testnet org.**
   - Press `s` and choose:
     - testnet
     - Full Validators
     - keys generated on the host
     - your own org plus SDF's testnet quorum preset
     - local archives served by nginx
   - Review the per-host diff, press `D` and confirm.
   - All three hosts finish the rolling deploy and reach `Synced!`.
   - The audit log (`l`) has every step.
7. **No generated seed on the laptop.** On each VM, read the seed with
   `sudo grep NODE_SEED /etc/stellar/stellar-core.cfg`. Then on the laptop,
   without the seed ever reaching shell history:

   ```sh
   read -rs SEED            # paste the seed (no echo, no history), enter
   grep -rlF -- "$SEED" "$XDG_CONFIG_HOME" "$XDG_STATE_HOME" ~/.ssh ~/.config ~/.local ~/.cache /tmp 2>/dev/null
   unset SEED
   ```

   It prints nothing for all three seeds.
8. **A broken config rolls back.**
   - Start the wizard again for the org. On `acc-b`'s advanced step, set
     `HTTP_PORT` to a port another process holds. To get one, run
     `python3 -m http.server 11627` on `acc-b` and use `11627`.
   - Deploy. `acc-b` fails its health gate, the backup is restored
     automatically, and the node returns to `Synced!`.
   - The other hosts are not touched after the failure.
9. **Arm and clear an upgrade.**
   - Press `u`, then `a`. Paste an SDF-style string with a time a few
     hours ahead:

     ```
     upgrades?mode=set&upgradetime=<YYYY-MM-DDTHH:MM:SSZ>&basefee=100
     ```

   - Arm all three hosts. Each is confirmed, and the overview and each
     host's Upgrades tab show it as armed.
   - Then clear (`c`). The vote is gone on all three.
10. **An unsupported protocol is blocked.** Arm with `protocolversion` set
    one above the hosts' supported version. The review blocks it, naming
    each host and its maximum.
11. **The intersection banner.** Run `go test ./internal/app -run
    TestIntersectionLostRaisesBanner`. It passes, and its frame in
    `internal/app/testdata/TestIntersectionLostRaisesBanner.golden` shows
    `NO QUORUM INTERSECTION (acme-b)` across the top.
12. **80×24 and NO_COLOR.** Resize the terminal to 80×24 and visit every
    screen: dashboard, host picker, detail tabs, wizard, deploy, upgrades,
    audit log, help. Nothing is cut off. Repeat with `NO_COLOR=1 auriga`:
    there is no colour anywhere, and every status still reads as a symbol
    and a word.

## Results

| # | Item | Result | Notes |
| - | ---- | ------ | ----- |
| 1 | Three hosts at once, one via ProxyJump | | |
| 2 | Unknown and changed host keys | | |
| 3 | Live dashboard, stale values | | |
| 4 | sshd stop and recovery | | |
| 5 | Adopt without changes | | |
| 6 | Wizard and rolling deploy to `Synced!` | | |
| 7 | No generated seed locally | | |
| 8 | Broken config rolled back | | |
| 9 | Arm and clear an upgrade | | |
| 10 | Unsupported protocol blocked | | |
| 11 | Intersection banner | | |
| 12 | 80×24 and NO_COLOR | | |

Binary version: ______  stellar-core version: ______  Date: ______  By: ______
