#!/bin/sh
# Generates the throwaway client key the integration hosts trust. The key
# never leaves test/integration/.work (git-ignored).
set -eu
cd "$(dirname "$0")"
mkdir -p .work/keys
if [ ! -s .work/id_ed25519 ]; then
	ssh-keygen -q -t ed25519 -N '' -C auriga-itest -f .work/id_ed25519
fi
cp .work/id_ed25519.pub .work/keys/authorized_keys
chmod 644 .work/keys/authorized_keys
