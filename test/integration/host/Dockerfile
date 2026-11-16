# Test host for Auriga's integration suite: Ubuntu LTS with OpenSSH, sudo and
# stellar-core from apt.stellar.org (amd64 only; on arm64 machines Docker
# runs it under emulation). No systemd in a container: a systemctl shim
# reports whether stellar-core is running.
FROM --platform=linux/amd64 ubuntu:24.04

ARG STELLAR_CORE_VERSION=30.0.0-3642.0e49751aa.noble

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server sudo curl ca-certificates gnupg procps \
 && curl -fsS https://apt.stellar.org/SDF.asc | gpg --dearmor -o /usr/share/keyrings/sdf.gpg \
 && echo "deb [signed-by=/usr/share/keyrings/sdf.gpg] https://apt.stellar.org noble stable" > /etc/apt/sources.list.d/sdf.list \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends stellar-core=${STELLAR_CORE_VERSION} \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd -m -s /bin/bash op \
 && (id stellar >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin -d /var/lib/stellar stellar) \
 && mkdir -p /var/lib/stellar /etc/stellar \
 && chown stellar:stellar /var/lib/stellar

COPY test/integration/host/systemctl /usr/local/bin/systemctl
COPY test/integration/host/sshd_config /etc/ssh/sshd_config.d/auriga-test.conf
COPY test/integration/host/entrypoint.sh /entrypoint.sh
RUN chmod 755 /usr/local/bin/systemctl /entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
