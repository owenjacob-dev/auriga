#!/bin/sh
# Configures the test host from environment variables, then runs sshd.
#   SUDO_MODE=nopasswd|password   sudo for user op (password: op-sudo-pw)
#   NODE_SEED_VALUE, NODE_ALIAS   this validator's seed and alias
#   QUORUM_VALIDATORS             "NAME:PUBKEY ..." for [[VALIDATORS]] (own entry skipped)
#   PEERS                         "host:port host:port ..." for KNOWN_PEERS
#   NO_FORWARDING=1               sshd refuses port forwarding
#   NO_CURL=1                     remove curl (admin fallback uses http-command)
# Without NODE_SEED_VALUE the host is a plain SSH host (the bastion).
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

if [ "${NO_FORWARDING:-}" = 1 ]; then
	echo 'AllowTcpForwarding no' >/etc/ssh/sshd_config.d/00-no-forwarding.conf
fi
if [ "${NO_CURL:-}" = 1 ]; then
	rm -f /usr/bin/curl
fi

cfg=/etc/stellar/stellar-core.cfg
if [ -n "${NODE_SEED_VALUE:-}" ] && [ ! -e "$cfg" ]; then
	peers=""
	for p in ${PEERS:-}; do peers="$peers\"$p\", "; done
	{
		echo '# Integration test validator, written by entrypoint.sh'
		echo 'NETWORK_PASSPHRASE="Auriga Integration Network ; November 2026"'
		echo "NODE_SEED=\"${NODE_SEED_VALUE} ${NODE_ALIAS:-node}\""
		echo 'NODE_IS_VALIDATOR=true'
		echo 'NODE_HOME_DOMAIN="itest.example"'
		echo 'HTTP_PORT=11626'
		echo 'PUBLIC_HTTP_PORT=false'
		echo 'PEER_PORT=11625'
		echo 'DATABASE="sqlite3:///var/lib/stellar/stellar.db"'
		echo 'BUCKET_DIR_PATH="/var/lib/stellar/buckets"'
		# Absolute, as in SDF's example config: the packaged unit runs from /.
		echo 'LOG_FILE_PATH="/var/log/stellar/stellar-core.log"'
		echo "KNOWN_PEERS=[${peers%, }]"
		echo 'UNSAFE_QUORUM=true'
		echo ''
		echo '[[HOME_DOMAINS]]'
		echo 'HOME_DOMAIN="itest.example"'
		echo 'QUALITY="MEDIUM"'
		for v in ${QUORUM_VALIDATORS:-}; do
			# Siblings only: core rejects a NAME equal to the NODE_SEED alias.
			[ "${v%%:*}" = "${NODE_ALIAS:-node}" ] && continue
			echo ''
			echo '[[VALIDATORS]]'
			echo "NAME=\"${v%%:*}\""
			echo 'HOME_DOMAIN="itest.example"'
			echo "PUBLIC_KEY=\"${v#*:}\""
		done
		echo ''
		echo '[HISTORY.local]'
		echo 'get="cp /var/lib/stellar/history/{0} {1}"'
		echo 'put="cp {0} /var/lib/stellar/history/{1}"'
		echo 'mkdir="mkdir -p /var/lib/stellar/history/{0}"'
	} >"$cfg"
	install -d -o stellar -g stellar /var/log/stellar
	chown root:stellar "$cfg"
	chmod 640 "$cfg"
	run() { su -s /bin/sh stellar -c "cd / && $*"; }
	run "stellar-core --conf $cfg new-db" >/var/log/stellar-init.log 2>&1
	run "stellar-core --conf $cfg new-hist local" >>/var/log/stellar-init.log 2>&1
fi
unset NODE_SEED_VALUE

if [ -e "$cfg" ]; then
	touch /var/log/stellar-core.log && chown stellar:stellar /var/log/stellar-core.log
	# Like the packaged systemd unit: no WorkingDirectory, so run from /.
	su -s /bin/sh stellar -c "cd / && exec stellar-core --conf $cfg run" >>/var/log/stellar-core.log 2>&1 &
fi

ssh-keygen -A >/dev/null
exec /usr/sbin/sshd -D -e
