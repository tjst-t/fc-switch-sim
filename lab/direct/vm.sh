#!/bin/bash
# VM を 1 台起こす（up.sh から使う。単独でも：vm.sh 名前 TAP sshのポート MACの末尾）
set -euo pipefail
cd "$(dirname "$0")/../.."
R=lab/run; C=.cache; MTU=${MTU:-9000}
vm() { # 名前 TAP ssh のポート MAC の末尾
  local name=$1 tap=$2 port=$3 m=$4
  [ -f $R/$name.qcow2 ] || qemu-img create -q -f qcow2 -F qcow2 -b "$(pwd)/$C/fcsim-base.qcow2" $R/$name.qcow2
  printf 'instance-id: %s\nlocal-hostname: %s\n' $name $name > $R/$name-meta
  printf '#cloud-config\nhostname: %s\n' $name > $R/$name-user
  cloud-localds $R/$name-seed.iso $R/$name-user $R/$name-meta
  nohup qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 4096 -display none -monitor none \
    -drive file=$R/$name.qcow2,if=virtio -drive file=$R/$name-seed.iso,if=virtio,format=raw \
    -nic user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:$port-:22 \
    -netdev tap,id=fc0,ifname=$tap,script=no,downscript=no \
    -device virtio-net-pci,netdev=fc0,mac=52:54:00:fc:00:$m,host_mtu=$MTU \
    -serial file:$R/$name-console.log > $R/$name-qemu.log 2>&1 &
  echo $! > $R/$name.pid
}
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  [ -f $R/$1.pid ] && kill "$(cat $R/$1.pid)" 2>/dev/null && sleep 2 || true
  vm "$@"
fi
