#!/usr/bin/env bash
set -euo pipefail
: "${ASTILBA_DISPOSABLE_HOST:?Install only on the explicitly disposable native fixture host.}"
[[ $ASTILBA_DISPOSABLE_HOST == 1 && $EUID == 0 ]]
source /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]]
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-remove --no-install-recommends wireguard-tools=1.0.20210914-1ubuntu4 nftables=1.0.9-1ubuntu0.1
modprobe wireguard
