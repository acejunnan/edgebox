#!/bin/sh
set -eu
# Replace only our own table in one nftables transaction. Existing global
# rules, routing, and Ethernet traffic are outside this operation's scope.
if /usr/sbin/nft list table inet edgebox_management >/dev/null 2>&1; then
    { printf 'delete table inet edgebox_management\n'; cat /etc/edgebox/ap-isolation.nft; } | /usr/sbin/nft -f -
else
    /usr/sbin/nft -f /etc/edgebox/ap-isolation.nft
fi
