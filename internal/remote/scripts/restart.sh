# restart SERVICE
#
# Restarts (or starts) the service and prints its state.
service=$1
systemctl restart "$service" || return $?
sleep 1
printf 'active=%s\n' "$(systemctl is-active "$service" 2>/dev/null)"
