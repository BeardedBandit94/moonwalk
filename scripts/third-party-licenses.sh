#!/usr/bin/env bash
# Prints the licence of everything that is compiled into the moonwalk
# binary: each Go module it is built from, and Go itself. A release ships
# the result next to the binary, as those licences require.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "moonwalk is built from the following software. Each is listed with its"
echo "version and the licence it is distributed under."

section() {
  printf '\n\n%s\n%s\n\n' "$1" "$(printf '%s' "$1" | sed 's/./=/g')"
}

go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/moonwalk |
  sort -u |
  while read -r module version dir; do
    section "$module $version"
    found=0
    for file in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
      [ -f "$file" ] || continue
      found=1
      printf -- '--- %s ---\n\n' "$(basename "$file")"
      cat "$file"
      printf '\n'
    done
    if [ "$found" = 0 ]; then
      echo "No licence file found in $module $version" >&2
      exit 1
    fi
  done

section "The Go programming language ($(go env GOVERSION)), its runtime and standard library"
cat "$(go env GOROOT)/LICENSE"
