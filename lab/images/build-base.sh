#!/bin/bash
# 共通の VM イメージ（Debian 13 に fcoe-utils・targetcli・mhVTL を入れたもの）を作る。
# 出力：.cache/fcsim-base.qcow2（KVM が要る）
set -euo pipefail
cd "$(dirname "$0")/../.."
C=.cache
mkdir -p $C
[ -f $C/debian-13-generic-amd64.qcow2 ] || curl -fsSL -o $C/debian-13-generic-amd64.qcow2 \
  https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2
[ -f $C/id_lab ] || ssh-keygen -q -t ed25519 -N '' -f $C/id_lab
P1=$(base64 -w0 lab/images/base/patches/0001-mhvtl-lto10-support.patch)
P2=$(base64 -w0 lab/images/base/patches/0002-drives-1-2-use-lto10.patch)
sed -e "s|__SSH_PUBKEY__|$(cat $C/id_lab.pub)|" \
    -e "s|__PATCH1_B64__|$P1|" \
    -e "s|__PATCH2_B64__|$P2|" \
    lab/images/base/user-data > $C/base-user-data
printf 'instance-id: fcsim-base-%s\nlocal-hostname: fcsim-base\n' "$(date +%s)" > $C/base-meta-data
cloud-localds $C/base-seed.iso $C/base-user-data $C/base-meta-data
cp $C/debian-13-generic-amd64.qcow2 $C/fcsim-base.qcow2.tmp
qemu-img resize $C/fcsim-base.qcow2.tmp 12G
qemu-system-x86_64 -enable-kvm -cpu host -smp 4 -m 4096 -nographic \
  -drive file=$C/fcsim-base.qcow2.tmp,if=virtio \
  -drive file=$C/base-seed.iso,if=virtio,format=raw \
  -nic user,model=virtio-net-pci \
  -serial file:$C/base-build-console.log -monitor none -display none
mv $C/fcsim-base.qcow2.tmp $C/fcsim-base.qcow2
echo "できた：$C/fcsim-base.qcow2"
