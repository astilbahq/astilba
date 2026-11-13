#!/usr/bin/env bash
# Verifies an agent release built by .github/workflows/agent-release.yml and signs it offline; see
# the release procedure in docs/agent.md. Run it from the root of a checkout of main, after
# downloading the run's artifacts into amd64/ and arm64/:
#   scripts/agent-release-sign.sh agent-v0.3.0 /secure/release.pem ./releases
# It stops at the first check that fails, and signs nothing unless every check passed.
set -euo pipefail
(($# == 3)) || { echo 'Usage: scripts/agent-release-sign.sh TAG PRIVATE_KEY CATALOG_DIRECTORY' >&2; exit 2; }
tag=$1 key=$2 catalog=$3
repository=astilbahq/astilba
workflow=$repository/.github/workflows/agent-release.yml
refuse() { printf 'Refused: %s\n' "$*" >&2; exit 1; }
for tool in git gh jq go; do command -v "$tool" >/dev/null || refuse "$tool is needed."; done
[[ "$tag" == agent-v* ]] || refuse "$tag is not an agent-vVERSION tag."
version=${tag#agent-v}
key=$(cd "$(dirname "$key")" && pwd)/$(basename "$key")
catalog=$(mkdir -p "$catalog" && cd "$catalog" && pwd)

# The tag as it is on GitHub now, replacing any local copy, and main.
git fetch --quiet origin main "+refs/tags/$tag:refs/tags/$tag"
commit=$(git rev-parse --verify --quiet "refs/tags/$tag^{commit}") || refuse "$tag doesn't exist."
git merge-base --is-ancestor "$commit" origin/main || refuse "$tag ($commit) is not on main."
echo "$tag is $commit, on main."

for arch in amd64 arm64; do
  artifact=$PWD/$arch/astilba-agent-linux-$arch
  manifest=$artifact.json
  [[ -f "$artifact" && -f "$manifest" ]] || refuse "$artifact and its manifest are missing; download them with gh run download RUN -n agent-release-$arch -D $arch."
  [[ $(jq -r .commit "$manifest") == "$commit" ]] || refuse "the $arch manifest names another commit than $tag."
  [[ $(jq -r .version "$manifest") == "$version" ]] || refuse "the $arch manifest names another version than $tag."
  [[ $(jq -r .arch "$manifest") == "$arch" ]] || refuse "the $arch manifest is for another architecture."
  # Each file must be attested by the release workflow, run for this tag at this commit.
  for file in "$artifact" "$manifest"; do
    gh attestation verify "$file" --repo "$repository" --signer-workflow "$workflow" \
      --source-ref "refs/tags/$tag" --source-digest "$commit" >/dev/null || refuse "$file has no attestation from $workflow for $tag at $commit."
  done
  echo "The $arch artifact and manifest are attested for $tag."
  jq '{version, minimumVersion, commit, arch, size, sha256}' "$manifest"
done

# The signing tool is built from the release's own commit, in a separate worktree, never from an
# artifact under review. It checks that it was built from the manifest's commit and that the
# manifest's minimum version is the one that commit embeds.
tree=$(mktemp -d)
trap 'git worktree remove --force "$tree" >/dev/null 2>&1 || rm -rf "$tree"' EXIT
git worktree add --quiet --detach "$tree" "$commit"
(cd "$tree/services/platform" && CGO_ENABLED=0 go build -trimpath -buildvcs=true -o "$tree/astilba-agent" ./cmd/agent)
for arch in amd64 arm64; do
  "$tree/astilba-agent" release-sign --key "$key" --manifest "$PWD/$arch/astilba-agent-linux-$arch.json" \
    --artifact "$PWD/$arch/astilba-agent-linux-$arch" --out "$catalog" >/dev/null
  echo "Signed $tag for $arch into $catalog."
done
