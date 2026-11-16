#!/bin/sh
# Configures the test host from environment variables, then runs sshd.
#   SUDO_MODE=nopasswd|password   sudo for user op (password: op-sudo-pw)
#   NODE_SEED_VALUE               seed written to /etc/stellar/stellar-core.cfg
set -eu

install -d -m 700 -o op -g op /home/op/.ssh
install -m 600 -o op -g op /keys/authorized_keys /home/op/.ssh/authorized_keys

case "${SUDO_MODE:-nopasswd}" in
nopasswd) echo 'op ALL=(ALL) NOPASSWD: ALL' >/etc/sudoers.d/op ;;
password)
	echo 'op:op-sudo-pw' | chpasswd
	echo 'op ALL=(ALL) ALL' >/etc/sudoers.d/op
	;;
esac
chmod 440 /etc/sudoers.d/op

if [ -n "${NODE_SEED_VALUE:-}" ] && [ ! -e /etc/stellar/stellar-core.cfg ]; then
	cat >/etc/stellar/stellar-core.cfg <<CFG
NETWORK_PASSPHRASE="Test SDF Network ; September 2015"
NODE_SEED="${NODE_SEED_VALUE} ${NODE_ALIAS:-node}"
NODE_HOME_DOMAIN="itest.example"
NODE_IS_VALIDATOR=true
HTTP_PORT=11626
CFG
	chown root:stellar /etc/stellar/stellar-core.cfg
	chmod 640 /etc/stellar/stellar-core.cfg
fi
unset NODE_SEED_VALUE

ssh-keygen -A >/dev/null
exec /usr/sbin/sshd -D -e
