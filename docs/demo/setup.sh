#!/bin/sh
# Prepares the demo recorded in docs/demo.gif: brings up the Docker
# integration stack (three stellar-core 30 validators on a private network
# passphrase, one behind a bastion) and names them acme-a, acme-b and
# acme-c in ~/.ssh/auriga-demo.conf.
#
#   docs/demo/setup.sh          # then: vhs docs/demo/demo.tape
#   docs/demo/setup.sh down     # stop the stack, remove the demo files
#
# Auriga, like OpenSSH, reads ~/.ssh/config from your password-database home
# directory, so the demo hosts must be reachable from it. This script
# writes ~/.ssh/config only if you have none; otherwise add this line at the
# top of your config yourself:
#   Include ~/.ssh/auriga-demo.conf
#
# Never point this at real validators: the keys and hosts are test-only.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$root/test/integration/.work
state=$work/demo-state
ssh_home=$(getent passwd "$(id -un)" | cut -d: -f6)
demo_conf=$ssh_home/.ssh/auriga-demo.conf
demo_known=$ssh_home/.ssh/auriga-demo.known_hosts
marker="# written by auriga docs/demo/setup.sh"
compose="docker compose -f $root/test/integration/compose.yaml"

if [ "${1:-}" = down ]; then
	$compose down -v
	rm -f "$demo_conf" "$demo_known"
	if [ -f "$ssh_home/.ssh/config" ] && head -n 1 "$ssh_home/.ssh/config" | grep -qxF "$marker"; then
		rm -f "$ssh_home/.ssh/config"
	fi
	rm -rf "$state"
	exit 0
fi

"$root/test/integration/setup.sh"
$compose up -d --build --wait
make -C "$root" -s build

mkdir -p "$ssh_home/.ssh"
chmod 700 "$ssh_home/.ssh"
cat >"$demo_conf" <<CFG
Host acme-a
  HostName 127.0.0.1
  Port 2221
Host acme-b
  HostName 127.0.0.1
  Port 2222
Host acme-c
  HostName host-c
  ProxyJump demo-bastion
Host demo-bastion
  HostName 127.0.0.1
  Port 2220
Host acme-a acme-b acme-c demo-bastion
  User op
  IdentityFile $work/id_ed25519
  IdentitiesOnly yes
  UserKnownHostsFile $demo_known
  GlobalKnownHostsFile /dev/null
CFG
key() { $compose exec -T "$1" cat /etc/ssh/ssh_host_ed25519_key.pub | awk -v a="$2" '{print a, $1, $2}'; }
{
	key host-a '[127.0.0.1]:2221'
	key host-b '[127.0.0.1]:2222'
	key bastion '[127.0.0.1]:2220'
	key host-c host-c
} >"$demo_known"
chmod 600 "$demo_conf" "$demo_known"

if [ ! -e "$ssh_home/.ssh/config" ]; then
	printf '%s\nInclude %s\n' "$marker" "$demo_conf" >"$ssh_home/.ssh/config"
	chmod 600 "$ssh_home/.ssh/config"
elif ! grep -qF "$demo_conf" "$ssh_home/.ssh/config" && ! grep -qF "auriga-demo.conf" "$ssh_home/.ssh/config"; then
	echo "Add this line at the top of $ssh_home/.ssh/config, then run vhs:"
	echo "  Include ~/.ssh/auriga-demo.conf"
fi

rm -rf "$state"
mkdir -p "$state/config" "$state/state"
echo "demo ready; record with: vhs docs/demo/demo.tape"
