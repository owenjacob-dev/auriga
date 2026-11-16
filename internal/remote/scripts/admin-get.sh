# admin-get PORT COMMAND CONFIG_PATH
#
# Fallback transport for the stellar-core admin endpoint, used when sshd
# refuses port forwarding (SPEC.md "Reaching the admin HTTP endpoint").
# COMMAND is one of Auriga's six read-only admin commands with its query,
# already allowlisted by the caller. Prints the response body, then a line
# "@@status=<code> via=<curl|http-command>".
port=$1
cmd=$2
config=$3

if command -v curl >/dev/null 2>&1; then
	curl -sS -m 14 -w '\n@@status=%{http_code} via=curl\n' "http://127.0.0.1:$port/$cmd"
	return $?
fi
sudo -n -u stellar -- stellar-core --conf "$config" http-command "$cmd" || return $?
printf '\n@@status=200 via=http-command\n'
