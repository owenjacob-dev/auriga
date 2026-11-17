#!/bin/sh
# Prints the CHANGELOG.md section for one version, for the release body.
# usage: .github/release-notes.sh 0.1.0 [CHANGELOG.md]
set -eu
version=$1
file=${2:-CHANGELOG.md}
notes=$(awk -v v="$version" '
	/^## \[/ { if (on) exit; on = index($0, "## [" v "]") == 1; next }
	on
' "$file")
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "release-notes: no \"## [$version]\" section in $file" >&2
	exit 1
fi
printf '%s\n' "$notes"
