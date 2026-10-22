#!/bin/bash
# ContainerLab の構成の下ごしらえ：スイッチのイメージ、VM のディスク（共通イメージの差分）と cloud-init の種。
# 種には VM の中で流すスクリプトを入れ、起動したら自動で FC の設定まで済ませる。
#   prepare.sh [single|ha]
#     single：fc-tape.clab.yml    → run.clab.yml（サーバ 1・ライブラリ 1・スイッチ 1）
#     ha    ：fc-tape-ha.clab.yml → run-ha.clab.yml（サーバ 2・ライブラリ 2・ファブリック A と B）
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
CACHE=$ROOT/.cache
BASE=$CACHE/fcsim-base.qcow2
[ -f "$BASE" ] || { echo "$BASE が無い。先に lab/images/build-base.sh" >&2; exit 1; }
if [ "${REBUILD:-1}" = 1 ]; then
  sudo docker build -q -f "$ROOT/image/Dockerfile" -t fc-switch-sim/fcsw:dev "$ROOT" >/dev/null
fi
TOPO=${1:-single}
case "$TOPO" in
  single) SRC=fc-tape.clab.yml; OUT=run.clab.yml; RUN=run ;;
  ha) SRC=fc-tape-ha.clab.yml; OUT=run-ha.clab.yml; RUN=run-ha ;;
  *) echo "知らない構成 $TOPO（single か ha）" >&2; exit 2 ;;
esac
sed "s|__CACHE__|$CACHE|g" $SRC > $OUT
PUB=$(cat "$CACHE/id_lab.pub")
b64() { base64 -w0 "$1"; }
# FCSIM_DEBUG=1 なら、VM の中で libfc・libfcoe のデバッグのログを起動時から出す（調査用）
if [ "${FCSIM_DEBUG:-}" = 1 ]; then
  DEBUGCMD='modprobe libfcoe; echo 0x3f > /sys/module/libfc/parameters/debug_logging; echo 0xff > /sys/module/libfcoe/parameters/debug_logging'
else
  DEBUGCMD=true
fi
seed() { # 名前 役割の命令
  local name=$1 cmd=$2 d=$RUN/$1
  mkdir -p "$d"
  rm -f "$d/disk.qcow2"
  qemu-img create -q -f qcow2 -F qcow2 -b "$BASE" "$d/disk.qcow2" 20G
  cat > "$d/user-data" <<UD
#cloud-config
hostname: $name
ssh_authorized_keys:
  - $PUB
write_files:
  - path: /opt/fcsim/fcoe-up.sh
    permissions: "0755"
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/fcoe-up.sh")
  - path: /opt/fcsim/lib-setup.sh
    permissions: "0755"
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/lib-setup.sh")
runcmd:
  - [sh, -c, '$DEBUGCMD']
  - [sh, -c, '$cmd > /var/log/fcsim-setup.log 2>&1; echo \$? > /var/log/fcsim-setup.rc']
UD
  printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$name" "$(date +%s)" "$name" > "$d/meta-data"
  cat > "$d/network-config" <<'NC'
version: 2
ethernets:
  mgmt0:
    match:
      macaddress: "52:54:00:aa:00:01"
    set-name: mgmt0
    dhcp4: true
NC
  cloud-localds -N "$d/network-config" "$d/seed.iso" "$d/user-data" "$d/meta-data"
}
if [ "$TOPO" = single ]; then
  seed srv1 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:00:01"
  seed lib1 "/opt/fcsim/lib-setup.sh 52:54:00:fc:00:02 0x2000525400fc0001"
else
  # MAC：52:54:00:fc:<ファブリック 01=A・02=B>:<ノード 01=srv1・02=lib1・03=lib2>
  # ライブラリの LIO は両方のサーバを許す（どのサーバに見せるかはスイッチのゾーニングで決める）
  INIS="0x2000525400fc0101 0x2000525400fc0201 0x2000525400fc0104 0x2000525400fc0204"
  seed srv1 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:01:01,52:54:00:fc:02:01"
  seed srv2 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:01:04,52:54:00:fc:02:04"
  seed lib1 "/opt/fcsim/lib-setup.sh -i 1 52:54:00:fc:01:02,52:54:00:fc:02:02 $INIS"
  seed lib2 "/opt/fcsim/lib-setup.sh -i 2 52:54:00:fc:01:03,52:54:00:fc:02:03 $INIS"
fi
echo "用意できた。sudo containerlab deploy -t lab/clab/$OUT"
