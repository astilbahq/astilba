#!/usr/bin/env bash
set -euo pipefail
# Inspect this file first. It never installs or reconfigures Docker or ingress.
# Inputs must already be verified using the detached signed manifest.
if [[ $EUID != 0 || $# != 3 ]]; then
  echo 'Usage: sudo bash install.sh VERIFIED_AGENT RELEASE_PUBLIC_PEM SYSTEMD_UNIT' >&2
  exit 1
fi
if [[ ! -f /etc/os-release ]]; then exit 1; fi
source /etc/os-release
if [[ $ID != ubuntu || $VERSION_ID != 24.04 ]]; then
  echo 'Supported host: Ubuntu 24.04 LTS.' >&2; exit 1
fi
if [[ -e /usr/local/bin/astilba-agent || -e /etc/systemd/system/astilba-agent.service ]]; then
  echo 'An agent is already installed. Use the explicit local update procedure.' >&2; exit 1
fi
install -d -m 0700 /var/lib/astilba-agent
install -d -m 0755 /etc/astilba
install -m 0644 "$2" /etc/astilba/release-public.pem
install -m 0755 "$1" /usr/local/bin/astilba-agent
install -m 0644 "$3" /etc/systemd/system/astilba-agent.service
systemctl daemon-reload
/usr/local/bin/astilba-agent preflight
printf '%s\n' 'Installation prepared. Enroll using the dashboard code, then explicitly enable the service.'
