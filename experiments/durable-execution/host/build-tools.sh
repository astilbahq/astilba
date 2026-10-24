#!/usr/bin/env bash
set -euo pipefail

: "${PLAT4_DISPOSABLE_HOST:?Run only inside the disposable fixture VM.}"
[[ "$PLAT4_DISPOSABLE_HOST" == 1 && "$EUID" == 0 ]]
systemctl is-active --quiet astilba-plat4-docker.service
base=/opt/astilba-plat5
case "$(uname -m)" in
  x86_64)
    arch=amd64
    railarch=x86_64
    buildkit_sha=a59539daf547fe0958efe2d07e41c0a6944e1112a66d7e519ec1cf5b673ef15b
    zot_sha=33b9b5bfe05d6788c076de951a01920477dbd6395c330146e23cc2340073e52e
    railpack_sha=248a8f60f36854aabed6124113631078a802de3461660855fc4de9c26065c1fb
    ;;
  aarch64)
    arch=arm64
    railarch=arm64
    buildkit_sha=c8c97b84baf89dfe0ea85063ac24ad88eb909979ea476ce3ef9e8e11e60ebef8
    zot_sha=abc9bab13c144a4ee2f9067a6bbbe70ca1c018c49dbf521db28975514ea705da
    railpack_sha=732ece14eb4f4676a43ebba19431f954edf7b130acb76159ef0f2ff86ac42ac9
    ;;
  *) echo 'Unsupported fixture architecture' >&2; exit 1 ;;
esac
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
fetch() {
  curl --fail --location --silent --show-error "$1" --output "$scratch/$2"
  echo "$3  $scratch/$2" | sha256sum --check --status
}
fetch "https://github.com/moby/buildkit/releases/download/v0.34.1/buildkit-v0.34.1.linux-$arch.tar.gz" buildkit.tgz "$buildkit_sha"
fetch "https://github.com/project-zot/zot/releases/download/v2.1.22/zot-linux-$arch" zot "$zot_sha"
fetch "https://github.com/railwayapp/railpack/releases/download/v0.43.0/railpack-v0.43.0-$railarch-unknown-linux-musl.tar.gz" railpack.tgz "$railpack_sha"
install -d -m 0755 "$base/bin"
tar -xzf "$scratch/buildkit.tgz" -C "$scratch"
install -m 0755 "$scratch/bin/buildctl" "$base/bin/buildctl"
install -m 0755 "$scratch/zot" "$base/bin/zot"
tar -xzf "$scratch/railpack.tgz" -C "$scratch"
install -m 0755 "$scratch/railpack" "$base/bin/railpack"
"$base/bin/buildctl" --version
"$base/bin/zot" --version
"$base/bin/railpack" --version
