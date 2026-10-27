#!/bin/bash
# 共通の VM イメージ（Debian 13 に fcoe-utils・targetcli・mhVTL を入れたもの）を作る。
# 出力：.cache/fcsim-base.qcow2（KVM が要る）。FCSIM_BASE=<パス> で出力先を変えられる
# （動いている構成の VM のディスクは今のイメージの差分なので、上書きせずに別の名前で作るときに使う）。
set -euo pipefail
cd "$(dirname "$0")/../.."
C=.cache
mkdir -p $C
OUT=${FCSIM_BASE:-$C/fcsim-base.qcow2}
# Debian 13 のクラウドイメージは日付の版で固定し、SHA512 を確かめる（latest は日々変わるため）。
# 版を上げるときは、https://cloud.debian.org/images/cloud/trixie/<版>/SHA512SUMS から 2 つを書き換える。
DEBIAN_VER=20261021-2638
DEBIAN_SHA512=70ef6adc1c16155f8d575731b37d0f976d1c60b6144ac3f57914a3f4b30b202b69584d5d143aa39da0f0fe81df9e5b698909c0d94c7d91c6f6c4bb6581ead5f0
IMG=$C/debian-13-generic-amd64-$DEBIAN_VER.qcow2
if [ ! -f "$IMG" ]; then
  # 前の作り方で取ってある latest が、この版と同じなら使う
  if [ -f $C/debian-13-generic-amd64.qcow2 ] && echo "$DEBIAN_SHA512  $C/debian-13-generic-amd64.qcow2" | sha512sum -c --status; then
    cp $C/debian-13-generic-amd64.qcow2 "$IMG"
  else
    curl -fsSL -o "$IMG.part" \
      https://cloud.debian.org/images/cloud/trixie/$DEBIAN_VER/debian-13-generic-amd64-$DEBIAN_VER.qcow2
    mv "$IMG.part" "$IMG"
  fi
fi
echo "$DEBIAN_SHA512  $IMG" | sha512sum -c --status || { echo "$IMG の SHA512 が合わない" >&2; exit 1; }
[ -f $C/id_lab ] || ssh-keygen -q -t ed25519 -N '' -f $C/id_lab
P1=$(base64 -w0 lab/images/base/patches/0001-mhvtl-lto10-support.patch)
P2=$(base64 -w0 lab/images/base/patches/0002-drives-1-2-use-lto10.patch)
P3=$(base64 -w0 lab/images/base/patches/0003-changer-sim-fault-injection.patch)
sed -e "s|__SSH_PUBKEY__|$(cat $C/id_lab.pub)|" \
    -e "s|__PATCH1_B64__|$P1|" \
    -e "s|__PATCH2_B64__|$P2|" \
    -e "s|__PATCH3_B64__|$P3|" \
    lab/images/base/user-data > $C/base-user-data
printf 'instance-id: fcsim-base-%s\nlocal-hostname: fcsim-base\n' "$(date +%s)" > $C/base-meta-data
cloud-localds $C/base-seed.iso $C/base-user-data $C/base-meta-data
cp "$IMG" "$OUT.tmp"
qemu-img resize "$OUT.tmp" 12G
# VM は作り終えると電源を切る。固まったときのために 1 時間で打ち切る
timeout 3600 qemu-system-x86_64 -enable-kvm -cpu host -smp 4 -m 4096 -nographic \
  -drive file="$OUT.tmp",if=virtio \
  -drive file=$C/base-seed.iso,if=virtio,format=raw \
  -nic user,model=virtio-net-pci \
  -serial file:$C/base-build-console.log -monitor none -display none || true
# VM の中の結果（user-data の最後にシリアルコンソールへ出す印）を確かめる。失敗・印なしなら置き換えない
if grep -aq 'FCSIM-BUILD-RESULT=OK' $C/base-build-console.log; then
  mv "$OUT.tmp" "$OUT"
  echo "できた：$OUT"
else
  mv "$OUT.tmp" "$OUT.failed"
  echo "VM の中の build が失敗した（$OUT は置き換えていない。中身は $OUT.failed、ログは $C/base-build-console.log）：" >&2
  grep -a 'FCSIM-BUILD-' $C/base-build-console.log >&2 || echo "  終わりの印が無い（途中で止まった・時間切れ）" >&2
  exit 1
fi
