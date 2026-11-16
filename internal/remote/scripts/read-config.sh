# read-config CONFIG_PATH
#
# Prints the stellar-core config with the NODE_SEED value replaced by
# "<redacted:G…>", where G… is the public key derived on this host with
# `stellar-core sec-to-pub`. The seed is never printed, never passed in argv
# (printf is a shell builtin), and never leaves this host (SPEC.md "Reading a
# config without moving the seed").
#
# Output: header lines (pubkey=, seed=, node_alias=), a line "---", then the
# redacted file. Exit 3: config missing; 4: config unreadable.
config=$1

if [ ! -e "$config" ]; then
	echo "config $config not found" >&2
	return 3
fi
if [ ! -r "$config" ]; then
	echo "config $config is not readable" >&2
	return 4
fi

line=$(grep -m 1 -E '^[[:space:]]*NODE_SEED[[:space:]]*=' "$config" 2>/dev/null)
pubkey=
node_alias=
if [ -z "$line" ]; then
	seed_state=absent
else
	seed_state=present
	val=$(printf '%s\n' "$line" | sed -n -e "s/^[^=]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" -e "s/^[^=]*=[[:space:]]*'\([^']*\)'.*/\1/p" | head -n 1)
	seed=${val%% *}
	case "$val" in
	*" "*) node_alias=${val#* } ;;
	esac
	if [ -n "$seed" ]; then
		# sec-to-pub takes no --conf (stellar-core 30 rejects it).
		pubkey=$(printf '%s\n' "$seed" | sudo -n -u stellar -- stellar-core sec-to-pub 2>/dev/null | tail -n 1 | tr -d '[:space:]')
	fi
	unset seed val line
	if ! printf '%s' "$pubkey" | grep -Eq '^G[A-Z2-7]{55}$'; then
		pubkey=
	fi
fi

# A node alias is shown in the UI; keep only safe characters.
node_alias=$(printf '%s' "$node_alias" | tr -cd 'A-Za-z0-9._-')

printf 'pubkey=%s\n' "$pubkey"
printf 'seed=%s\n' "$seed_state"
printf 'node_alias=%s\n' "$node_alias"
printf -- '---\n'

marker="<redacted:${pubkey:-unknown}>"
if [ -n "$node_alias" ]; then
	marker="$marker $node_alias"
fi
# Replace every NODE_SEED line, then redact anything else shaped like a seed.
awk -v marker="$marker" '
	/^[[:space:]]*NODE_SEED[[:space:]]*=/ { print "NODE_SEED = \"" marker "\""; next }
	{ print }
' "$config" | sed -E 's/S[A-Z2-7]{55}/<redacted>/g'
