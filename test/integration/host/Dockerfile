# Test host for Auriga's integration suite: Ubuntu LTS with OpenSSH, sudo and
# a stellar user. M1 uses a stellar-core shim and a systemctl shim (no
# systemd in a container); M2 installs real stellar-core.
FROM ubuntu:24.04

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-server sudo \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd -m -s /bin/bash op \
 && useradd -r -s /usr/sbin/nologin -d /var/lib/stellar stellar \
 && mkdir -p /var/lib/stellar /etc/stellar \
 && chown stellar:stellar /var/lib/stellar

COPY internal/remote/testdata/shims/stellar-core /usr/local/bin/stellar-core
COPY internal/remote/testdata/shims/systemctl /usr/local/bin/systemctl
COPY test/integration/host/sshd_config /etc/ssh/sshd_config.d/auriga-test.conf
COPY test/integration/host/entrypoint.sh /entrypoint.sh
RUN chmod 755 /usr/local/bin/stellar-core /usr/local/bin/systemctl /entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
