# check-archive WELL_KNOWN_URL BINARY...
#
# Checks, as the stellar user, that the programs an archive's commands use
# exist, and whether the archive already exists (its
# .well-known/stellar-history.json answers). Prints "bin=<name>:<yes|no>"
# lines and "exists=<yes|no|unknown>".
url=$1
shift
for b in "$@"; do
	if sudo -n -u stellar -- sh -c 'command -v "$1"' sh "$b" >/dev/null 2>&1; then
		printf 'bin=%s:yes\n' "$b"
	else
		printf 'bin=%s:no\n' "$b"
	fi
done
if command -v curl >/dev/null 2>&1; then
	if curl -sf -m 10 -o /dev/null "$url"; then
		echo exists=yes
	else
		echo exists=no
	fi
else
	echo exists=unknown
fi
