# gen-seed CONFIG_PATH
#
# Generates a seed on this host for a deploy and keeps it in a root-only
# pending file next to the config, so the deploy can render configs with
# its public key before staging. Prints only the public key. Running it
# again reuses the pending seed.
config=$1
pending=$config.auriga-pending-seed
umask 077
if [ -s "$pending" ]; then
	IFS= read -r seed <"$pending"
else
	out=$(sudo -n -u stellar -- stellar-core gen-seed 2>/dev/null)
	seed=$(printf '%s\n' "$out" | sed -n 's/^Secret seed: *//p' | head -n 1)
	unset out
	case "$seed" in
	S*) ;;
	*) echo "stellar-core gen-seed printed no seed" >&2; return 4 ;;
	esac
	printf '%s\n' "$seed" >"$pending.tmp" && chmod 600 "$pending.tmp" && mv -f "$pending.tmp" "$pending" || return 5
fi
pub=$(printf '%s\n' "$seed" | sudo -n -u stellar -- stellar-core sec-to-pub 2>/dev/null | tail -n 1 | tr -d '[:space:]')
unset seed
printf 'pubkey=%s\n' "$pub"
