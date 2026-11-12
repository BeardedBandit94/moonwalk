#!/usr/bin/env bash
# Prints the Homebrew formula for a release that build-release.sh has built:
#
#   scripts/homebrew-formula.sh 0.1.0 dist > moonwalk.rb
#
# The formula installs the release's own binaries, so it needs the release
# to be published under the same version on GitHub.
set -euo pipefail

version=${1:?usage: homebrew-formula.sh <version> <directory>}
dist=${2:?usage: homebrew-formula.sh <version> <directory>}
version=${version#v}
repository=${GITHUB_REPOSITORY:-BeardedBandit94/moonwalk}
base=https://github.com/$repository/releases/download/v$version

checksum() {
  awk -v file="moonwalk_${version}_$1.tar.gz" '$2 == file { print $1 }' "$dist/moonwalk_${version}_checksums.txt" | grep . ||
    { echo "no checksum for $1 in $dist" >&2; exit 1; }
}

cat <<FORMULA
class Moonwalk < Formula
  desc "Explorer for Terraform and OpenTofu code, state and the live cloud"
  homepage "https://github.com/$repository"
  version "$version"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "$base/moonwalk_${version}_darwin_arm64.tar.gz"
      sha256 "$(checksum darwin_arm64)"
    end
    on_intel do
      url "$base/moonwalk_${version}_darwin_amd64.tar.gz"
      sha256 "$(checksum darwin_amd64)"
    end
  end

  on_linux do
    on_arm do
      url "$base/moonwalk_${version}_linux_arm64.tar.gz"
      sha256 "$(checksum linux_arm64)"
    end
    on_intel do
      url "$base/moonwalk_${version}_linux_amd64.tar.gz"
      sha256 "$(checksum linux_amd64)"
    end
  end

  def install
    bin.install "moonwalk"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/moonwalk version")
  end
end
FORMULA
