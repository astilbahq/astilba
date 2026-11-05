#!/usr/bin/env bash
set -euo pipefail
: "${ASTILBA_DISPOSABLE_HOST:?Install only on the explicitly disposable native fixture host.}"
[[ "$ASTILBA_DISPOSABLE_HOST" == 1 && "$EUID" == 0 ]]
case "$(uname -m)" in
 x86_64) arch=amd64; sha=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c ;;
 aarch64) arch=arm64; sha=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465 ;;
 *) exit 1 ;;
esac
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
url="https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_$arch.bz2"
echo "Downloading $url"
# A release host's outage can last minutes, so these attempts span at least four. Only a
# failed download repeats; a file that fails its checksum stops the script.
curl --fail --location --silent --show-error --connect-timeout 10 --speed-limit 1024 --speed-time 60 --retry 8 --retry-all-errors --retry-max-time 300 "$url" -o "$scratch/restic.bz2" ||
  { echo "Download failed after retries: $url" >&2; exit 1; }
echo "$sha  $scratch/restic.bz2" | sha256sum --check --status ||
  { echo "Checksum mismatch for $url: expected $sha, got $(sha256sum "$scratch/restic.bz2" | cut -d' ' -f1)" >&2; exit 1; }
bzip2 -d "$scratch/restic.bz2"
install -d -m 0755 /opt/astilba-platform-tools
install -m 0755 "$scratch/restic" /opt/astilba-platform-tools/restic
/opt/astilba-platform-tools/restic version
