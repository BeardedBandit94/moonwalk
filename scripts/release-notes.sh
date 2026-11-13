#!/usr/bin/env bash
# Prints the notes of one release: its section of CHANGELOG.md, without the
# heading.
#
#   scripts/release-notes.sh 0.2.0
#
# It prints nothing, and fails, where the changelog has no such section, so
# that a release is not published without its notes. A pre-release with no
# section of its own gets the notes of the release it leads to.
set -euo pipefail

version=${1:?usage: release-notes.sh <version>}
version=${version#v}

cd "$(dirname "$0")/.."

section() {
  awk -v heading="## v$1" '
    $0 == heading { found = 1; next }
    found && /^## / { exit }
    found { print }
  ' CHANGELOG.md | sed -e '/./,$!d'
}

notes=$(section "$version")
# A pre-release, such as 0.2.0-rc1, has the notes of the release it leads to.
if [ -z "$notes" ] && [ "${version%%-*}" != "$version" ]; then
  notes=$(section "${version%%-*}")
fi
if [ -z "$notes" ]; then
  echo "CHANGELOG.md has no section for v$version" >&2
  exit 1
fi
printf '%s\n' "$notes"
