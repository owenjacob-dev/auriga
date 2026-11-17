# admin-get PORT COMMAND RAW_COMMAND CONFIG_PATH SERVICE
#
# Fallback transport for the stellar-core admin endpoint, used when sshd
# refuses port forwarding (SPEC.md "Reaching the admin HTTP endpoint").
# COMMAND is one of Auriga's six admin commands with its URL-encoded query,
# already allowlisted by the caller; RAW_COMMAND is the same with the query
# decoded, for http-command, which does not decode it. Prints the response
# body, then a line "@@status=<code> via=<curl|http-command>".
port=$1
cmd=$2
raw=$3
config=$4
service=$5

if command -v curl >/dev/null 2>&1; then
	curl -sS -m 14 -w '\n@@status=%{http_code} via=curl\n' "http://127.0.0.1:$port/$cmd"
	return $?
fi
auriga_core "$service" --conf "$config" http-command "$raw" || return $?
printf '\n@@status=200 via=http-command\n'
