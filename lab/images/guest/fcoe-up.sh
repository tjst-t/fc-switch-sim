#!/bin/bash
# VM の中で：FC 用の NIC で FCoE（ファブリックモード、VLAN 無し）を有効にする。
#   fcoe-up.sh <NIC>[,<NIC>...] [MTU]
# NIC は名前（ens4）でも MAC（52:54:00:fc:00:01）でも渡せる。冗長構成ではファブリックごとに 1 つ。
set -euo pipefail
resolve_nic() {
  case "$1" in
    *:*:*:*:*:*) ip -o link | awk -v m="$(echo "$1" | tr A-F a-f)" 'index($0, "link/ether " m) {sub(":$", "", $2); print $2; exit}' ;;
    *) echo "$1" ;;
  esac
}
MTU=${2:-9000}
NICS=()
for n in $(echo "${1:?NIC}" | tr ',' ' '); do
  r=$(resolve_nic "$n")
  [ -n "$r" ] || { echo "NIC $n が見つからない" >&2; exit 2; }
  NICS+=("$r")
done
modprobe fcoe
for NIC in "${NICS[@]}"; do
  ip link set "$NIC" mtu "$MTU" up
  # ソフトウェア FCoE はスキャッタギャザーが使えると、データのページをコピーせず NIC に渡す。
  # st（テープ）の大きなバッファのページでこれをすると、1MB 以上のブロックでカーネルが落ちた
  # （Bad page state → panic、Debian 13 の 6.12）。コピーの経路を使わせる。
  ethtool -K "$NIC" sg off >/dev/null 2>&1 || echo "注意：$NIC のスキャッタギャザーを切れなかった" >&2
  cat > /etc/fcoe/cfg-"$NIC" <<CFG
FCOE_ENABLE="yes"
DCB_REQUIRED="no"
AUTO_VLAN="no"
MODE="fabric"
CFG
done
systemctl enable --now fcoe-utils >/dev/null 2>&1
systemctl restart fcoe-utils
# NIC ごとの fc_host（WWPN = 0x2000 + MAC）が Online になるまで待つ
for NIC in "${NICS[@]}"; do
  want=0x2000$(tr -d : < /sys/class/net/$NIC/address)
  for i in $(seq 1 40); do
    st=""
    for h in /sys/class/fc_host/host*; do
      [ "$(cat $h/port_name 2>/dev/null)" = "$want" ] && st=$(cat $h/port_state)
    done
    [ "$st" = Online ] && break
    sleep 1
  done
done
for h in /sys/class/fc_host/host*; do
  echo "$h port_name=$(cat $h/port_name) port_id=$(cat $h/port_id) fabric=$(cat $h/fabric_name) state=$(cat $h/port_state)"
done
