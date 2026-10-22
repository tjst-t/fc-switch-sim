#!/bin/bash
# 直結の試験の場：サーバ VM と テープライブラリ VM を TAP でスイッチ（fcsw）につなぐ。
#   管理：ssh -p 2221（サーバ）・2222（ライブラリ）、ユーザ lab
#   スイッチの API：http://127.0.0.1:8080/api/v1/
set -euo pipefail
cd "$(dirname "$0")/../.."
R=lab/run; C=.cache
mkdir -p $R
MTU=${MTU:-9000}
for t in tap-srv tap-lib; do
  sudo ip link del $t 2>/dev/null || true
  sudo ip tuntap add $t mode tap user "$(id -un)"
  sudo ip link set $t mtu $MTU up
done
export PATH=$PATH:/usr/local/go/bin
go build -o bin/fcsw ./cmd/fcsw
sudo pkill -f 'bin/fcsw' 2>/dev/null || true
sudo nohup bin/fcsw -config lab/direct/switch.yaml -listen 127.0.0.1:8080 -debug > $R/fcsw.log 2>&1 &
source lab/direct/vm.sh
vm srv tap-srv 2221 01
vm lib tap-lib 2222 02
echo "起動した。ssh -i $C/id_lab -p 2221 lab@127.0.0.1 など"
