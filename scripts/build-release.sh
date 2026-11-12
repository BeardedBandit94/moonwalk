#!/usr/bin/env bash
# Builds the archives of one release into a directory, with their checksums:
#
#   scripts/build-release.sh 0.1.0 dist
#
# An archive holds the binary, the licence, the README and the licences of
# what the binary is built from. Nothing is published; the release workflow
# does that with what this leaves behind.
set -euo pipefail

version=${1:?usage: build-release.sh <version> <directory>}
out=${2:?usage: build-release.sh <version> <directory>}
version=${version#v}

cd "$(dirname "$0")/.."
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

scripts/third-party-licenses.sh > "$work/THIRD_PARTY_LICENSES"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  name=moonwalk_${version}_${os}_${arch}
  dir=$work/$name
  binary=moonwalk
  if [ "$os" = windows ]; then
    binary=moonwalk.exe
  fi
  mkdir "$dir"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$dir/$binary" ./cmd/moonwalk
  cp LICENSE README.md "$work/THIRD_PARTY_LICENSES" "$dir/"
  if [ "$os" = windows ]; then
    (cd "$dir" && zip -q "$out/$name.zip" "$binary" LICENSE README.md THIRD_PARTY_LICENSES)
  else
    tar -C "$dir" -czf "$out/$name.tar.gz" "$binary" LICENSE README.md THIRD_PARTY_LICENSES
  fi
  echo "built $name"
done

(cd "$out" && sha256sum moonwalk_"${version}"_*.tar.gz moonwalk_"${version}"_*.zip > "moonwalk_${version}_checksums.txt")
