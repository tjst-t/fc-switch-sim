#!/bin/bash
# fc-tape.clab.yml の下ごしらえ：スイッチのイメージ、VM のディスク（共通イメージの差分）と cloud-init の種。
# 種には VM の中で流すスクリプトを入れ、起動したら自動で FC の設定まで済ませる。
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
CACHE=$ROOT/.cache
BASE=$CACHE/fcsim-base.qcow2
[ -f "$BASE" ] || { echo "$BASE が無い。先に lab/images/build-base.sh" >&2; exit 1; }
if [ "${REBUILD:-1}" = 1 ]; then
  sudo docker build -q -f "$ROOT/image/Dockerfile" -t fc-switch-sim/fcsw:dev "$ROOT" >/dev/null
fi
sed "s|__CACHE__|$CACHE|g" fc-tape.clab.yml > run.clab.yml
PUB=$(cat "$CACHE/id_lab.pub")
b64() { base64 -w0 "$1"; }
seed() { # 名前 役割の命令
  local name=$1 cmd=$2 d=run/$1
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
seed srv1 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:00:01"
seed lib1 "/opt/fcsim/lib-setup.sh 52:54:00:fc:00:02 0x2000525400fc0001"
echo "用意できた。sudo containerlab deploy -t lab/clab/run.clab.yml"
