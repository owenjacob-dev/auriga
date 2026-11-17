# stage-backup CONFIG_PATH BACKUP_PATH
#
# Stages a backup for a manual rollback: copies it, seed and all, to
# <config>.auriga-staged on this host. Prints the staged file's sha256.
config=$1
backup=$2
case "$backup" in
"$config".auriga-*.bak) ;;
*) echo "not a backup of $config" >&2; return 2 ;;
esac
[ -f "$backup" ] || { echo "backup $backup not found" >&2; return 3; }
cp -p "$backup" "$config.auriga-staged.new" && mv -f "$config.auriga-staged.new" "$config.auriga-staged" || return 5
printf 'sha256=%s\n' "$(sha256sum "$config.auriga-staged" | cut -d ' ' -f 1)"
