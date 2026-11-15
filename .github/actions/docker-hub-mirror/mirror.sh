#!/usr/bin/env bash
# Adds Google's public Docker Hub mirror to a disposable GitHub-hosted runner's own Docker daemon,
# keeping the rest of its configuration, and restarts the daemon. CI only: a customer's host keeps
# its own daemon configuration. The PLAT-4 fixture daemon the native lanes use configures the same
# mirror itself, in experiments/durable-execution/host/bootstrap.sh.
#
# Docker uses a mirror only for Docker Hub images, and checks what it serves against the pinned
# digest, so the mirror cannot change an image. When the mirror lacks an image or fails, Docker
# reads Docker Hub itself.
set -euo pipefail

if [[ "${GITHUB_ACTIONS:-}" != true || "${RUNNER_ENVIRONMENT:-}" != github-hosted || "$EUID" != 0 ]]; then
  echo "This rewrites the Docker daemon's configuration; run it only as root on a disposable GitHub-hosted runner." >&2
  exit 1
fi

mirror=https://mirror.gcr.io
config=/etc/docker/daemon.json
current='{}'
if [[ -s "$config" ]]; then
  current=$(cat "$config")
fi
updated=$(jq --arg mirror "$mirror" '.["registry-mirrors"] = ([$mirror] + ((.["registry-mirrors"] // []) - [$mirror, $mirror + "/"]))' <<<"$current")
install -d -m 0755 /etc/docker
printf '%s\n' "$updated" >"$config.astilba-new"
mv "$config.astilba-new" "$config"
echo "Docker daemon configuration:"
cat "$config"

systemctl restart docker.service
# docker.service is Type=notify, so the daemon answers once restart returns; the loop covers a
# socket that is still being recreated.
for _ in $(seq 30); do
  if mirrors=$(docker info --format '{{json .RegistryConfig.Mirrors}}' 2>/dev/null); then
    break
  fi
  sleep 1
done
if [[ "${mirrors:-}" != *'"https://mirror.gcr.io/"'* ]]; then
  echo "The Docker daemon does not list $mirror as a Docker Hub mirror after restarting: ${mirrors:-no answer}" >&2
  exit 1
fi
echo "Docker Hub mirrors: $mirrors"
