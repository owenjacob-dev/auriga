# backup CONFIG_PATH
#
# Copies the current config to <config>.auriga-<UTC>.bak with the same
# owner and mode, and keeps the ten newest backups. Prints "backup=<path>",
# empty when there is no config yet.
config=$1
if [ ! -e "$config" ]; then
	echo backup=
	return 0
fi
b=$config.auriga-$(date -u +%Y%m%dT%H%M%SZ).bak
[ -e "$b" ] && sleep 1 && b=$config.auriga-$(date -u +%Y%m%dT%H%M%SZ).bak
cp -p "$config" "$b" || return 5
echo "backup=$b"
ls -1 "$config".auriga-*.bak 2>/dev/null | sort -r | tail -n +11 | while IFS= read -r old; do
	rm -f -- "$old"
done
