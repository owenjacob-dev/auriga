# install CONFIG_PATH
#
# Moves the staged config over the config (an atomic rename on the same
# file system) and removes the pending seed, now consumed.
config=$1
[ -f "$config.auriga-staged" ] || { echo "nothing staged" >&2; return 3; }
mv -f "$config.auriga-staged" "$config" || return 5
rm -f "$config.auriga-pending-seed"
printf 'sha256=%s\n' "$(sha256sum "$config" | cut -d ' ' -f 1)"
