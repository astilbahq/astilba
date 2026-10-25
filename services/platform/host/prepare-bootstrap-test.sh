#!/usr/bin/env bash
set -euo pipefail
# A destructive fixture prerequisite, never a customer installation step.
: "${ASTILBA_DISPOSABLE_HOST:?Set only on an explicitly disposable Ubuntu host.}"
: "${ASTILBA_BOOTSTRAP_OWNER:?Provide a unique test ownership marker.}"
[[ "$ASTILBA_DISPOSABLE_HOST" == 1 && "$EUID" == 0 ]]
if [[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
  [[ "${GITHUB_RUN_ID:-}" =~ ^[0-9]+$ ]]
else
  [[ -f /etc/astilba-p14-owner && $(cat /etc/astilba-p14-owner) == "$ASTILBA_BOOTSTRAP_OWNER" ]] || { echo 'Disposable VM ownership is not established.' >&2; exit 1; }
fi
[[ ! -e /etc/astilba-bootstrap-test-owner && ! -L /etc/astilba-bootstrap-test-owner ]]
if command -v docker >/dev/null; then
  docker --host unix:///var/run/docker.sock info >/dev/null
  [[ -z "$(docker --host unix:///var/run/docker.sock ps --all --quiet)$(docker --host unix:///var/run/docker.sock volume ls --quiet)" ]]
  [[ -z $(docker --host unix:///var/run/docker.sock network ls --format '{{.Name}}' | sed '/^bridge$/d; /^host$/d; /^none$/d') ]]
  mapfile -t packages < <(dpkg-query -W -f='${binary:Package} ${db:Status-Status}\n' docker.io docker-ce docker-ce-cli docker-ce-rootless-extras docker-buildx-plugin docker-compose-plugin containerd containerd.io runc 2>/dev/null | awk '$2=="installed" {print $1}')
  ((${#packages[@]})) || { echo 'Cannot establish ownership of the preinstalled runtime packages.' >&2; exit 1; }
  systemctl stop docker.service docker.socket
  if systemctl cat containerd.service >/dev/null 2>&1; then systemctl stop containerd.service; fi
  DEBIAN_FRONTEND=noninteractive apt-get purge --yes "${packages[@]}"
  # Only the explicitly owned runner's inspected, unused default data paths.
  rm -rf /var/lib/docker /var/lib/containerd /etc/docker
  [[ ! -L /var/run/docker.sock ]]
  if [[ -S /var/run/docker.sock ]]; then
    ! systemctl is-active --quiet docker.socket
    rm /var/run/docker.sock
  fi
fi
for path in /var/lib/docker /var/lib/containerd /usr/local/bin/astilba-agent /var/lib/astilba-agent /etc/astilba/release-public.pem; do
  [[ ! -e "$path" && ! -L "$path" ]]
done
printf '%s\n' "$ASTILBA_BOOTSTRAP_OWNER" > /etc/astilba-bootstrap-test-owner
