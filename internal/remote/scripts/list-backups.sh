# list-backups CONFIG_PATH
#
# Lists Auriga's backups of the config (<config>.auriga-<UTC>.bak) as
# tab-separated "backup=<path> <mtime> <size> <owner> <mode>" lines.
config=$1
dir=$(dirname "$config")
base=$(basename "$config")
for f in "$dir/$base".auriga-*.bak; do
	[ -f "$f" ] || continue
	printf 'backup=%s\t%s\t%s\t%s\t%s\n' "$f" "$(stat -c %Y "$f")" "$(stat -c %s "$f")" "$(stat -c %U:%G "$f")" "$(stat -c %a "$f")"
done
