#!/bin/sh
# Prints the CHANGELOG.md section for one version, for the release body.
# usage: .github/release-notes.sh 1.0.0 [CHANGELOG.md]
#
# A final version needs its own "## [X.Y.Z]" section. A pre-release
# (1.0.0-rc.1) uses its own section if there is one, and otherwise the
# "## [Unreleased]" section, so release candidates can be cut before the
# changelog is moved under the final version.
set -eu
version=$1
file=${2:-CHANGELOG.md}

section() {
	awk -v v="$1" '
		/^## \[/ { if (on) exit; on = index($0, "## [" v "]") == 1; next }
		on
	' "$file" | sed '/./,$!d'
}
empty() { [ -z "$(printf '%s' "$1" | tr -d '[:space:]')" ]; }

notes=$(section "$version")
case $version in
*-*)
	if empty "$notes"; then
		notes=$(section Unreleased)
		if ! empty "$notes"; then
			notes=$(printf 'Release candidate for %s. Changes since the last release:\n\n%s' "${version%%-*}" "$notes")
		fi
	fi
	;;
esac
if empty "$notes"; then
	echo "release-notes: no \"## [$version]\" section in $file" >&2
	exit 1
fi
printf '%s\n' "$notes"
