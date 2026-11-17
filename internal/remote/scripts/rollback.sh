# rollback CONFIG_PATH SERVICE [BACKUP_PATH]
#
# Restores BACKUP_PATH over the config (copy, then atomic rename) and
# restarts the service. Without a backup (a new node had no config), the
# failed config is moved aside and the service stopped.
config=$1
service=$2
backup=$3
rm -f "$config.auriga-staged"
if [ -z "$backup" ]; then
	systemctl stop "$service" 2>/dev/null
	if [ -e "$config" ]; then
		mv -f "$config" "$config.auriga-failed-$(date -u +%Y%m%dT%H%M%SZ)" || return 5
	fi
	echo active=stopped
	return 0
fi
case "$backup" in
"$config".auriga-*.bak) ;;
*) echo "not a backup of $config" >&2; return 2 ;;
esac
[ -f "$backup" ] || { echo "backup $backup not found" >&2; return 3; }
cp -p "$backup" "$config.auriga-restore" && mv -f "$config.auriga-restore" "$config" || return 5
systemctl restart "$service" || return $?
sleep 1
printf 'active=%s\n' "$(systemctl is-active "$service" 2>/dev/null)"
