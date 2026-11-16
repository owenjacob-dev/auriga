# probe CONFIG_PATH SERVICE
#
# Describes the host for Auriga's inventory as key=value lines on stdout.
# Runs as root via sudo. Prints no secrets: the config is only inspected for
# its mode and for BUCKET_DIR_PATH and HTTP_PORT.
config=$1
service=$2

kv() {
	printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr '\n\r' '  ')"
}

conf_value() {
	# Value of a top-level KEY = "string" or KEY = number line; empty if absent.
	sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\{0,1\}\([^\"#]*\)\"\{0,1\}.*/\1/p" "$config" 2>/dev/null | head -n 1 | sed 's/[[:space:]]*$//'
}

kv hostname "$(hostname 2>/dev/null)"
if [ -r /etc/os-release ]; then
	kv os "$(. /etc/os-release && printf '%s' "${PRETTY_NAME:-unknown}")"
fi

if id stellar >/dev/null 2>&1; then
	kv stellar_user yes
else
	kv stellar_user no
fi

if command -v stellar-core >/dev/null 2>&1; then
	kv core_installed yes
	if [ -r "$config" ]; then
		version=$(auriga_core "$service" --conf "$config" version 2>/dev/null | head -n 1)
	else
		version=$(auriga_core "$service" version 2>/dev/null | head -n 1)
	fi
	kv core_version "$version"
else
	kv core_installed no
fi

if command -v systemctl >/dev/null 2>&1; then
	kv service_load "$(systemctl show -p LoadState --value "$service" 2>/dev/null)"
	kv service_active "$(systemctl is-active "$service" 2>/dev/null)"
	kv service_enabled "$(systemctl is-enabled "$service" 2>/dev/null)"
fi
# Where the service runs, and so where relative config paths point.
workdir=$(auriga_workdir "$service")
kv service_workdir "$workdir"

if [ -e "$config" ]; then
	kv config_present yes
	if [ -r "$config" ]; then kv config_readable yes; else kv config_readable no; fi
	kv config_mode "$(stat -c '%a' "$config" 2>/dev/null)"
	kv config_owner "$(stat -c '%U:%G' "$config" 2>/dev/null)"
	kv config_sha256 "$(sha256sum "$config" 2>/dev/null | cut -d ' ' -f 1)"
	kv http_port "$(conf_value HTTP_PORT)"
	bucket=$(conf_value BUCKET_DIR_PATH)
else
	kv config_present no
fi
[ -n "$bucket" ] || bucket=buckets
case "$bucket" in
/*) ;;
*) bucket=${workdir%/}/$bucket ;;
esac
# df needs an existing path: walk up to the nearest existing parent.
dir=$bucket
while [ ! -e "$dir" ] && [ "$dir" != / ]; do
	dir=$(dirname "$dir")
done
kv bucket_dir "$bucket"
set -- $(df -P -k "$dir" 2>/dev/null | tail -n 1)
kv disk_total_kb "$2"
kv disk_avail_kb "$4"

if command -v timedatectl >/dev/null 2>&1; then
	kv ntp_synchronized "$(timedatectl show -p NTPSynchronized --value 2>/dev/null)"
else
	kv ntp_synchronized unknown
fi
