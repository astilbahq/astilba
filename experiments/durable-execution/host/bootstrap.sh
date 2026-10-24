#!/usr/bin/env bash
set -euo pipefail

# Only for a fresh, disposable Ubuntu VM. Never run on a deployment server.
: "${PLAT4_DISPOSABLE_HOST:?Set PLAT4_DISPOSABLE_HOST=1 only inside a disposable VM.}"
[[ "$PLAT4_DISPOSABLE_HOST" == 1 && "$EUID" == 0 ]]
source /etc/os-release
[[ "$ID" == ubuntu && "$VERSION_ID" == 24.04 ]]
[[ "$(cat /proc/1/comm)" == systemd ]]

base=/opt/astilba-plat4
if [[ -e /etc/systemd/system/astilba-plat4-docker.service ]]; then
  echo "This fixture daemon is already installed; reuse it or recreate the VM." >&2
  exit 1
fi

if command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  if [[ -n "$(docker ps --all --quiet)$(docker volume ls --quiet)" ]] ||
    [[ -n "$(docker network ls --format '{{.Name}}' | sed '/^bridge$/d; /^host$/d; /^none$/d')" ]]; then
    echo "Refusing to replace a Docker daemon with existing workloads or data." >&2
    exit 1
  fi
  systemctl stop docker.service docker.socket
fi

case "$(uname -m)" in
  x86_64)
    arch=amd64
    docker_arch=x86_64
    docker_sha=9ac225999fd5e863f7694c728ef23d480616b1e5639328fa309befd4243d69eb
    compose_sha=47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0
    go_sha=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
    ;;
  aarch64)
    arch=arm64
    docker_arch=aarch64
    docker_sha=873b3e0207857c3b2f28392fb922f3ea6efe37620c16d4d4aaf2fef409c90b73
    compose_sha=42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d
    go_sha=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49
    ;;
  *) echo "Only amd64 and arm64 are in this experiment." >&2; exit 1 ;;
esac

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
fetch() {
  curl --fail --location --silent --show-error "$1" --output "$scratch/$2"
  echo "$3  $scratch/$2" | sha256sum --check --status
}
fetch "https://download.docker.com/linux/static/stable/$docker_arch/docker-29.9.2.tgz" docker.tgz "$docker_sha"
fetch "https://github.com/docker/compose/releases/download/v5.6.1/docker-compose-linux-$docker_arch" compose "$compose_sha"
fetch "https://go.dev/dl/go1.27.3.linux-$arch.tar.gz" go.tgz "$go_sha"
install -d -m 0755 "$base/bin" "$base/cli-plugins"
tar -xzf "$scratch/docker.tgz" -C "$scratch"
install -m 0755 "$scratch"/docker/* "$base/bin/"
install -m 0755 "$scratch/compose" "$base/cli-plugins/docker-compose"
tar -xzf "$scratch/go.tgz" -C "$base"

cat > /etc/systemd/system/astilba-plat4-docker.service <<'UNIT'
[Unit]
Description=Disposable Astilba PLAT-4 Docker daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
Environment=PATH=/opt/astilba-plat4/bin:/usr/sbin:/usr/bin:/sbin:/bin
ExecStart=/opt/astilba-plat4/bin/dockerd --host=unix:///run/astilba-plat4/docker.sock --data-root=/var/lib/astilba-plat4/docker --exec-root=/run/astilba-plat4/exec --pidfile=/run/astilba-plat4/docker.pid --group=root --exec-opt native.cgroupdriver=systemd
RuntimeDirectory=astilba-plat4
Delegate=yes
TasksMax=infinity
LimitNOFILE=1048576
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now astilba-plat4-docker.service
"$base/bin/docker" --host unix:///run/astilba-plat4/docker.sock version --format '{{.Server.Version}}'
"$base/cli-plugins/docker-compose" version --short
"$base/go/bin/go" version
