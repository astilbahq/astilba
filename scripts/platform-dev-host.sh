#!/usr/bin/env bash
# Prepares the disposable Colima VM that `make dev` runs its agent on. It arrives on
# stdin as root inside that VM, never on a real server: see docs/local-development.md.
set -euo pipefail
umask 077
: "${ASTILBA_DISPOSABLE_HOST:?Run only inside the disposable local development VM.}"
[[ "$ASTILBA_DISPOSABLE_HOST" == 1 && "$EUID" == 0 ]] || exit 1
[[ $# == 2 && "$2" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || {
  echo 'Usage: platform-dev-host.sh claim|prepare|agent OWNER_UUID' >&2
  exit 2
}
mode=$1 owner=$2
marker=/etc/astilba-dev/owner
backups=/var/lib/astilba-dev-backups

# Every later step changes this VM, so it must belong to this checkout's stack.
if [[ "$mode" == claim ]]; then
  [[ ! -e /etc/astilba-dev && ! -e /var/lib/astilba-agent && ! -e /usr/local/bin/astilba-agent ]] ||
    { echo 'This VM already holds Astilba state; refusing to claim it.' >&2; exit 1; }
  install -d -m 0755 /etc/astilba-dev
  printf '%s\n' "$owner" > "$marker"
  chmod 0644 "$marker"
  exit 0
fi
[[ "$mode" == prepare || "$mode" == agent ]] || exit 2
[[ -f "$marker" && "$(cat "$marker")" == "$owner" ]] ||
  { echo 'This VM belongs to another local stack; refusing to change it.' >&2; exit 1; }

# Installs the staged agent build: the first time through the product's own installer,
# afterwards by replacing the executable while the service is stopped.
if [[ "$mode" == agent ]]; then
  stage=/var/lib/astilba-dev
  # Only this VM trusts the stack's management certificate; the Mac's trust store is untouched.
  if ! cmp --silent "$stage/management.pem" /usr/local/share/ca-certificates/astilba-dev-management.crt; then
    install -m 0644 "$stage/management.pem" /usr/local/share/ca-certificates/astilba-dev-management.crt
    (umask 022; update-ca-certificates >/dev/null)
  fi
  if [[ ! -e /usr/local/bin/astilba-agent ]]; then
    bash "$stage/install.sh" "$stage/astilba-agent" "$stage/release-public.pem" "$stage/astilba-agent.service" >/dev/null
  elif ! cmp --silent "$stage/astilba-agent" /usr/local/bin/astilba-agent; then
    systemctl stop astilba-agent
    install -m 0755 "$stage/astilba-agent" /usr/local/bin/astilba-agent
  fi
  install -d -m 0755 /etc/systemd/system/astilba-agent.service.d
  cat > /etc/systemd/system/astilba-agent.service.d/local-development.conf <<UNIT
[Service]
ExecStart=
ExecStart=/usr/local/bin/astilba-agent run --restic-cli /opt/astilba-platform-tools/restic --backup-roots $backups
ReadWritePaths=$backups
UNIT
  chmod 0644 /etc/systemd/system/astilba-agent.service.d/local-development.conf
  systemctl daemon-reload
  exit 0
fi

# The agent requires exactly the Compose release the guided bootstrap installs. These are
# that bootstrap's pinned packages; platform-dev.test.mjs keeps the two in step.
case "$(uname -m)" in
  aarch64) arch=arm64 compose_sha=d138ccdb343121de4904f53af5c57baee5866f9aedc142c1f8c0d6448ce57a7b ;;
  x86_64) arch=amd64 compose_sha=9725be1df7fd5ccc6b21ac24112885896c8d134345e3f4aa20b3c2064ef4620e ;;
  *) echo "Unsupported VM architecture: $(uname -m)" >&2; exit 1 ;;
esac
compose=$(docker --host unix:///var/run/docker.sock compose version --short 2>/dev/null || true)
if [[ "${compose#v}" != 5.6.1 ]]; then
  scratch=$(mktemp -d)
  trap 'rm -rf "$scratch"' EXIT
  file="docker-compose-plugin_5.6.1-1~ubuntu.24.04~noble_$arch.deb"
  url="https://download.docker.com/linux/ubuntu/dists/noble/pool/stable/$arch/$file"
  echo "Installing Docker Compose 5.6.1 from $url"
  curl --fail --location --silent --show-error --proto '=https' --connect-timeout 10 --max-time 300 \
    --retry 5 --retry-all-errors "$url" -o "$scratch/$file"
  printf '%s  %s\n' "$compose_sha" "$scratch/$file" | sha256sum --check --status ||
    { echo "Checksum mismatch for $url" >&2; exit 1; }
  DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-remove --no-install-recommends "$scratch/$file"
  [[ "$(docker compose version --short)" == 5.6.1 || "$(docker compose version --short)" == v5.6.1 ]]
fi

# Lima answers on its gateway address for every port the Mac has open on loopback. Deployed
# containers must not reach the developer's own services that way. The agent itself
# reaches management through a tunnel to this VM's loopback, not through the gateway.
gateway=$(getent ahostsv4 host.lima.internal | awk 'NR == 1 { print $1 }')
[[ "$gateway" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Cannot resolve the Lima gateway.' >&2; exit 1; }
iptables -C DOCKER-USER -d "$gateway" -j REJECT 2>/dev/null ||
  iptables -I DOCKER-USER -d "$gateway" -j REJECT

# The repository's restic installer needs bzip2, which Colima's image leaves out.
if ! command -v bzip2 >/dev/null; then
  timeout --foreground --kill-after=30 300 env DEBIAN_FRONTEND=noninteractive apt-get update >/dev/null
  DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-remove --no-install-recommends bzip2 >/dev/null
fi

# The only filesystem backup root the local agent is allowed.
install -d -m 0700 "$backups"
