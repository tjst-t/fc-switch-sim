#!/bin/bash
# テープライブラリの VM の中で：mhVTL を起こし、LIO（tcm_fc）で FC のターゲットとして見せ、FCoE を有効にする。
#   lib-setup.sh [-i <ライブラリの番号 1〜9>] <NIC>[,<NIC>...] <許すイニシエータの WWPN（0x... か naa.…）>...
# NIC は名前でも MAC でも。冗長構成では NIC（ファブリック）ごとにターゲットを作り、同じ装置を両方から見せる。
# -i を付けると、シリアル番号・NAA・テープのバーコードをライブラリごとに変える（複数台を同じサーバから見るとき）：
#   シリアル番号 XYZZY_A → LIB<番号>_A、NAA の 5 バイト目 ab → a<番号>、バーコード E01 → E<番号>1
# 順番が大事：ターゲットを作ってから FCoE を有効にする（あとからだと相手の libfc が古い役割を覚えたままになる）。
set -euo pipefail
resolve_nic() {
  case "$1" in
    *:*:*:*:*:*) ip -o link | awk -v m="$(echo "$1" | tr A-F a-f)" 'index($0, "link/ether " m) {sub(":$", "", $2); print $2; exit}' ;;
    *) echo "$1" ;;
  esac
}
LIBID=""
if [ "${1:-}" = -i ]; then LIBID=$2; shift 2; fi
NICARG=${1:?NIC}; shift
NICS=()
for n in $(echo "$NICARG" | tr ',' ' '); do
  r=$(resolve_nic "$n")
  [ -n "$r" ] || { echo "NIC $n が見つからない" >&2; exit 2; }
  NICS+=("$r")
done
[ $# -ge 1 ] || { echo "イニシエータの WWPN が要る" >&2; exit 2; }
# やり直しのときは、先に FCoE を外す（使用中のターゲットを消すと tcm_fc が固まることがある）
if ls /sys/class/fc_host/ 2>/dev/null | grep -q host; then
  for NIC in "${NICS[@]}"; do fcoeadm -d "$NIC" 2>/dev/null || true; done
  sleep 2
fi
# 起動時に保存済みの LIO の設定を戻させない（mhVTL の装置ができる前に戻そうとして失敗し、順番も崩れる）
systemctl disable rtslib-fb-targetctl >/dev/null 2>&1 || true
# 1. mhVTL
BARCODE=E01
if [ -n "$LIBID" ]; then
  BARCODE=E${LIBID}1
  if [ ! -f /etc/mhvtl/.fcsim-id ]; then
    sed -i -e "s/XYZZY_A/LIB${LIBID}_A/g" -e "s/^\( NAA: 10:22:33:44:\)ab:/\1a${LIBID}:/" /etc/mhvtl/device.conf
    sed -i "s/E01/${BARCODE}/g" /etc/mhvtl/library_contents.10
    echo "$LIBID" > /etc/mhvtl/.fcsim-id
  fi
fi
if [ ! -d /opt/mhvtl/${BARCODE}001L8 ]; then
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
serial() { sg_vpd -p sn "$1" 2>/dev/null | awk -F: '/serial number:/ {gsub(/ /,"",$2); print $2}' | tail -1; }
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
WWPNS=""
for NIC in "${NICS[@]}"; do WWPNS="$WWPNS,naa.2000$(tr -d : < /sys/class/net/$NIC/address)"; done
python3 - "${WWPNS#,}" "$LIBSG" "$*" $DRIVES <<'PY'
import sys
from rtslib_fb import RTSRoot, FabricModule, Target, TPG, LUN, NodeACL, MappedLUN, PSCSIStorageObject
wwpns, libsg, inis, drives = sys.argv[1].split(","), sys.argv[2], sys.argv[3].split(), sys.argv[4:]
root = RTSRoot()
root.clear_existing(confirm=True)
sos = [PSCSIStorageObject("lib" if i == 0 else f"drv{i}", dev=dev) for i, dev in enumerate([libsg] + drives)]
for wwpn in wwpns:
    t = Target(FabricModule("tcm_fc"), wwpn, mode="create")
    tpg = TPG(t, 1, mode="create")
    tpg.enable = True
    luns = [LUN(tpg, i, so) for i, so in enumerate(sos)]
    for ini in inis:
        ini = ini.lower().removeprefix("0x").removeprefix("naa.").replace(":", "")
        acl = NodeACL(tpg, "naa." + ini, mode="create")
        for lun in luns:
            MappedLUN(acl, lun.lun, lun)
root.save_to_file()
print(f"ターゲット {wwpns}：ライブラリ {libsg}、ドライブ {len(drives)} 台、許すイニシエータ {inis}")
PY
# 3. FCoE
"$(dirname "$0")"/fcoe-up.sh "$(IFS=,; echo "${NICS[*]}")"
