#!/usr/bin/env bash
set -euo pipefail
umask 077
# Inspect this optional preparation before --apply. It does not enroll peers.
[[ $# == 1 && ( $1 == --check || $1 == --apply ) ]] || { echo 'Usage: sudo bash network-tools.sh --check|--apply' >&2; exit 1; }
source /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 && $(cat /proc/1/comm) == systemd ]] || { echo 'Ubuntu 24.04 LTS with systemd is required.' >&2; exit 1; }
[[ -x /usr/local/bin/astilba-agent && -f /etc/systemd/system/astilba-agent.service ]] || { echo 'Install the signed network-capable agent first.' >&2; exit 1; }
/usr/local/bin/astilba-agent network-support >/dev/null
for path in /var/lib/astilba-agent /etc/systemd/system/astilba-agent.service.d /etc/systemd/system/astilba-network.service; do
  [[ ! -L $path ]] || { echo 'Refusing a symbolic link at a managed network installation path.' >&2; exit 1; }
done
if [[ -e /etc/systemd/system/astilba-agent.service.d/network.conf || -e /etc/systemd/system/astilba-network.service ]]; then
  echo 'Network service configuration already exists. Inspect it; this preparation will not overwrite it.' >&2; exit 1
fi
printf '%s\n' 'Managed networking adds pinned WireGuard/nft tools, a persistent private-link restore service and AF_NETLINK permission for the agent. Peers, private addresses, UDP listener and workload grants require separate owner review in the dashboard. Allow the selected UDP listener through your upstream firewall. Existing firewall policies remain in effect.'
if [[ $1 == --check ]]; then exit 0; fi
[[ $EUID == 0 ]] || { echo 'Apply requires root.' >&2; exit 1; }
# Ubuntu's signed package indexes authenticate these exact supported versions.
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-remove --no-install-recommends wireguard-tools=1.0.20210914-1ubuntu4 nftables=1.0.9-1ubuntu0.1
modprobe wireguard
install -d -m 0700 /var/lib/astilba-agent
install -d -m 0755 /etc/systemd/system/astilba-agent.service.d
printf '[Service]\nRestrictAddressFamilies=\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK\n' > /etc/systemd/system/astilba-agent.service.d/network.conf
cat > /etc/systemd/system/astilba-network.service <<'UNIT'
__ASTILBA_NETWORK_UNIT__
UNIT
printf 'astilba-managed-network-v1\n' > /var/lib/astilba-agent/network-enabled
chmod 0600 /var/lib/astilba-agent/network-enabled
systemctl daemon-reload
systemctl enable astilba-network.service
printf '%s\n' 'Private-link preparation complete. Restart the agent at a safe idle point, then configure managed network membership in the dashboard.'
