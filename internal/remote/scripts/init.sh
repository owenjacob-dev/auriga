# init CONFIG_PATH SERVICE KIND [ARCHIVE]
#
# One-off initialisation with the service stopped: KIND new-db, or new-hist
# for ARCHIVE. A rollback cannot undo either.
config=$1
service=$2
kind=$3
archive=$4
systemctl stop "$service" 2>/dev/null
case "$kind" in
new-db) auriga_core "$service" --conf "$config" new-db ;;
new-hist) auriga_core "$service" --conf "$config" new-hist "$archive" ;;
*) echo "unknown init $kind" >&2; return 2 ;;
esac
