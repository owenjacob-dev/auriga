# preflight CONFIG_PATH SERVICE
#
# Checks before a deploy: the unit exists, free space where the config and
# the service's data live, and leftovers from an interrupted deploy.
config=$1
service=$2
kv() { printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr '\n\r' '  ')"; }
kv unit_load "$(systemctl show -p LoadState --value "$service" 2>/dev/null)"
kv active "$(systemctl is-active "$service" 2>/dev/null)"
if [ -e "$config" ]; then kv config_present yes; else kv config_present no; fi
dir=$(dirname "$config")
set -- $(df -P -k "$dir" 2>/dev/null | tail -n 1)
kv config_fs_avail_kb "$4"
wd=$(auriga_workdir "$service")
set -- $(df -P -k "$wd" 2>/dev/null | tail -n 1)
kv workdir_avail_kb "$4"
if [ -e "$config.auriga-staged" ]; then kv staged_leftover yes; else kv staged_leftover no; fi
if [ -s "$config.auriga-pending-seed" ]; then kv pending_seed yes; else kv pending_seed no; fi
