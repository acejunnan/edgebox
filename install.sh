#!/bin/bash
# Source bootstrap. A pinned commit may be supplied as EDGEBOX_REF.
set -euo pipefail
[[ ${EUID} == 0 ]] || { echo 'Run with sudo bash.' >&2; exit 1; }
role=${1:-}
[[ $role == vps || $role == n1 ]] || { echo 'Usage: install.sh vps|n1' >&2; exit 1; }
[[ -f /etc/debian_version && -d /run/systemd/system ]] || { echo 'Debian/Ubuntu/Armbian with systemd required.' >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl git python3 iproute2 nftables wireguard-tools
work=$(mktemp -d /var/tmp/edgebox-install.XXXXXXXX)
trap 'rm -rf -- "$work"' EXIT
git clone --depth 1 https://github.com/acejunnan/edgebox.git "$work/source"
if [[ -n ${EDGEBOX_REF:-} ]]; then
  [[ $EDGEBOX_REF =~ ^[a-f0-9]{40}$ ]] || { echo 'EDGEBOX_REF must be a full commit SHA.' >&2; exit 1; }
  git -C "$work/source" fetch --depth 1 origin "$EDGEBOX_REF"
  git -C "$work/source" checkout --detach FETCH_HEAD
fi
python3 "$work/source/deploy/provision.py" "$role"
