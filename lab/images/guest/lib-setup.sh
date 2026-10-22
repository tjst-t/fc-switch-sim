#!/bin/bash
# テープライブラリの VM の中で：mhVTL を起こし、LIO（tcm_fc）で FC のターゲットとして見せ、FCoE を有効にする。
#   lib-setup.sh <NIC> <許すイニシエータの WWPN（0x... か naa.…）>...
# 順番が大事：ターゲットを作ってから FCoE を有効にする（あとからだと相手の libfc が古い役割を覚えたままになる）。
set -euo pipefail
# NIC は名前（ens4）でも MAC（52:54:00:fc:00:01）でも渡せる
resolve_nic() {
  case "$1" in
    *:*:*:*:*:*) ip -o link | awk -v m="$(echo "$1" | tr A-F a-f)" 'index($0, "link/ether " m) {sub(":$", "", $2); print $2; exit}' ;;
    *) echo "$1" ;;
  esac
}
NIC=$(resolve_nic "${1:?NIC}"); shift
[ -n "$NIC" ] || { echo "NIC $1 が見つからない" >&2; exit 2; }
[ $# -ge 1 ] || { echo "イニシエータの WWPN が要る" >&2; exit 2; }
# やり直しのときは、先に FCoE を外す（使用中のターゲットを消すと tcm_fc が固まることがある）
if [ -d /sys/class/fc_host ] && ls /sys/class/fc_host/ 2>/dev/null | grep -q host; then
  fcoeadm -d "$NIC" 2>/dev/null || true
  sleep 2
fi
# 1. mhVTL
if [ ! -d /opt/mhvtl/E01001L8 ]; then
  make_vtl_media --config-dir=/etc/mhvtl --home-dir=/opt/mhvtl --force >/var/log/fcsim-media.log 2>&1
  chown -R vtl:vtl /opt/mhvtl 2>/dev/null || true
fi
systemctl start mhvtl.target
# ロボットとドライブが出そろうまで待つ（数が 3 秒変わらなくなるまで）
prev=-1; same=0
for i in $(seq 1 60); do
  n=$(lsscsi -g | grep -c -E "mediumx|tape" || true)
  if [ "$n" -gt 0 ] && [ "$n" = "$prev" ]; then same=$((same+1)); [ $same -ge 3 ] && break; else same=0; fi
  prev=$n; sleep 1
done
# 2. ライブラリ（STK L700）と、それに属するドライブ（シリアル番号の頭がライブラリと同じ）を /dev/sg で探す
LIBSG=$(lsscsi -g | awk '/mediumx/ && /L700/ {print $NF; exit}')
serial() { sg_vpd -p sn "$1" 2>/dev/null | awk -F: '/serial/ {gsub(/ /,"",$2); print $2}'; }
LIBSER=$(serial "$LIBSG")
DRIVES=""
for sg in $(lsscsi -g | awk '/tape/ {print $NF}'); do
  ser=$(serial "$sg")
  case "$ser" in "$LIBSER"[0-9]*) DRIVES="$DRIVES $ser:$sg";; esac
done
DRIVES=$(echo $DRIVES | tr ' ' '\n' | sort | cut -d: -f2 | tr '\n' ' ')
modprobe tcm_fc; modprobe target_core_pscsi
# FC の WWPN：fcoe が NIC の MAC から作るのと同じ規則（0x2000 + MAC）。
# targetcli は fc_host ができるまで tcm_fc を見せないので、rtslib を直に使う。
MAC=$(cat /sys/class/net/$NIC/address | tr -d :)
WWPN=naa.2000$MAC
python3 - "$WWPN" "$LIBSG" "$*" $DRIVES <<'PY'
import sys
from rtslib_fb import RTSRoot, FabricModule, Target, TPG, LUN, NodeACL, MappedLUN, PSCSIStorageObject
wwpn, libsg, inis, drives = sys.argv[1], sys.argv[2], sys.argv[3].split(), sys.argv[4:]
root = RTSRoot()
root.clear_existing(confirm=True)
t = Target(FabricModule("tcm_fc"), wwpn, mode="create")
tpg = TPG(t, 1, mode="create")
tpg.enable = True
luns = []
for i, dev in enumerate([libsg] + drives):
    so = PSCSIStorageObject("lib" if i == 0 else f"drv{i}", dev=dev)
    luns.append(LUN(tpg, i, so))
for ini in inis:
    ini = ini.lower().removeprefix("0x").removeprefix("naa.").replace(":", "")
    acl = NodeACL(tpg, "naa." + ini, mode="create")
    for lun in luns:
        MappedLUN(acl, lun.lun, lun)
root.save_to_file()
print(f"ターゲット {wwpn}：ライブラリ {libsg}、ドライブ {len(drives)} 台、許すイニシエータ {inis}")
PY
# 3. FCoE
"$(dirname "$0")"/fcoe-up.sh "$NIC"
