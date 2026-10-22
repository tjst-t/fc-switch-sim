#!/bin/bash
# VM の中で：FC 用の NIC で FCoE（ファブリックモード、VLAN 無し）を有効にする。
#   fcoe-up.sh <NIC> [MTU]
set -euo pipefail
NIC=${1:?NIC}; MTU=${2:-9000}
ip link set "$NIC" mtu "$MTU" up
# ソフトウェア FCoE はスキャッタギャザーが使えると、データのページをコピーせず NIC に渡す。
# st（テープ）の大きなバッファのページでこれをすると、1MB 以上のブロックでカーネルが落ちた
# （Bad page state → panic、Debian 13 の 6.12）。コピーの経路を使わせる。
ethtool -K "$NIC" sg off >/dev/null 2>&1 || echo "注意：$NIC のスキャッタギャザーを切れなかった" >&2
modprobe fcoe
cat > /etc/fcoe/cfg-"$NIC" <<CFG
FCOE_ENABLE="yes"
DCB_REQUIRED="no"
AUTO_VLAN="no"
MODE="fabric"
CFG
systemctl enable --now fcoe-utils >/dev/null 2>&1
systemctl restart fcoe-utils
for i in $(seq 1 30); do
  st=$(cat /sys/class/fc_host/host*/port_state 2>/dev/null | head -1 || true)
  [ "$st" = Online ] && break
  sleep 1
done
for h in /sys/class/fc_host/host*; do
  echo "$h port_name=$(cat $h/port_name) port_id=$(cat $h/port_id) state=$(cat $h/port_state)"
done
