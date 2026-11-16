# stage CONFIG_PATH MODE CONFIG_B64
#
# Writes the rendered config to <config>.auriga-staged (root:stellar 0640)
# with its seed marker replaced by the real seed. MODE says where the seed
# comes from: keep (the current config), generate (the pending seed from
# gen-seed), import (stdin, requested with auriga_read_secret) or none (no
# NODE_SEED). The seed only ever lives in this shell: it is substituted with
# shell built-ins, never passed to another program's arguments. Prints the
# staged file's sha256 and the seed's public key.
config=$1
mode=$2
b64=$3
staged=$config.auriga-staged
umask 077
printf '%s' "$b64" | base64 -d >"$staged.tmp" 2>/dev/null || { echo "the rendered config is not valid base64" >&2; return 5; }

seed=
case "$mode" in
keep)
	line=$(grep -m 1 -E '^[[:space:]]*NODE_SEED[[:space:]]*=' "$config" 2>/dev/null)
	val=$(printf '%s\n' "$line" | sed -n -e "s/^[^=]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" -e "s/^[^=]*=[[:space:]]*'\([^']*\)'.*/\1/p" | head -n 1)
	seed=${val%% *}
	unset line val
	;;
generate)
	[ -s "$config.auriga-pending-seed" ] || { echo "no pending seed; run gen-seed first" >&2; return 6; }
	IFS= read -r seed <"$config.auriga-pending-seed"
	;;
import)
	auriga_read_secret seed
	;;
none) ;;
*) echo "unknown seed mode $mode" >&2; return 2 ;;
esac
if [ "$mode" != none ]; then
	case "$seed" in
	S*) ;;
	*) echo "no usable seed for mode $mode" >&2; return 6 ;;
	esac
fi

replaced=0
while IFS= read -r l || [ -n "$l" ]; do
	case "$l" in
	'NODE_SEED = "<auriga-seed:'*)
		rest=${l#*>}
		printf 'NODE_SEED = "%s%s\n' "$seed" "$rest"
		replaced=$((replaced + 1))
		;;
	*) printf '%s\n' "$l" ;;
	esac
done <"$staged.tmp" >"$staged.new"
rm -f "$staged.tmp"
if [ "$mode" != none ] && [ "$replaced" -ne 1 ]; then
	rm -f "$staged.new"
	echo "expected one seed marker, found $replaced" >&2
	return 7
fi

pub=
if [ -n "$seed" ]; then
	pub=$(printf '%s\n' "$seed" | sudo -n -u stellar -- stellar-core sec-to-pub 2>/dev/null | tail -n 1 | tr -d '[:space:]')
fi
unset seed
chown root:stellar "$staged.new" && chmod 640 "$staged.new" && mv -f "$staged.new" "$staged" || return 5
printf 'sha256=%s\n' "$(sha256sum "$staged" | cut -d ' ' -f 1)"
printf 'pubkey=%s\n' "$pub"
