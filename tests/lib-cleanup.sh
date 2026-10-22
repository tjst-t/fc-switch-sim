#!/bin/bash
# 試験で投入したテープ（バーコードが I・Z で始まる）を、ライブラリから取り出して消す。
#   tests/lib-cleanup.sh [ライブラリの IP（既定 172.30.31.22）] [サーバの IP（既定 172.30.31.21）]
# サーバがロボットでメールスロットへ移し、ライブラリの VM で vtlcmd の empty map をして、テープのファイルを消す。
set -uo pipefail
cd "$(dirname "$0")/.."
LIB=${1:-172.30.31.22}; SRV=${2:-172.30.31.21}
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
SER=$(curl -s -m 5 http://$LIB:8080/api/v1/library | python3 -c 'import json,sys; print(json.load(sys.stdin)["library"]["serial"])')
ID=$(curl -s -m 5 http://$LIB:8080/api/v1/library | python3 -c 'import json,sys; print(json.load(sys.stdin)["library"]["id"])')
CH=$(ssh "${SSHO[@]}" lab@$SRV "for s in \$(lsscsi -g | awk '/mediumx/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw $SER && { echo \$s; break; }; done")
[ -n "$CH" ] || { echo "サーバからライブラリ $SER のロボットが見えない" >&2; exit 1; }
n=0
while :; do
  ST=$(ssh "${SSHO[@]}" lab@$SRV "sudo mtx -f $CH status")
  IES=$(awk -F'[ :]+' '/IMPORT\/EXPORT:Empty/ {print $4}' <<<"$ST")
  SRC=$(awk -F'[ :]+' '/Storage Element [0-9]+:Full :VolumeTag=[IZ][0-9]/ && !/IMPORT/ {print $4}' <<<"$ST")
  [ -n "$SRC" ] || break
  for s in $SRC; do
    d=$(head -1 <<<"$IES"); [ -n "$d" ] || break
    ssh "${SSHO[@]}" lab@$SRV "sudo mtx -f $CH transfer $s $d" >/dev/null && n=$((n+1))
    IES=$(tail -n +2 <<<"$IES")
  done
  ssh "${SSHO[@]}" lab@$LIB "sudo vtlcmd $ID open map >/dev/null; sudo vtlcmd $ID empty map >/dev/null; sudo vtlcmd $ID close map >/dev/null"
done
# メールスロットに残っているもの（試験のテープ）も出す
ssh "${SSHO[@]}" lab@$LIB "sudo vtlcmd $ID open map >/dev/null; sudo vtlcmd $ID empty map >/dev/null; sudo vtlcmd $ID close map >/dev/null; sudo sh -c 'rm -rf /opt/mhvtl/[IZ][0-9][0-9][0-9][0-9][0-9]L8'"
echo "ライブラリ $SER：試験のテープ $n 本を保管スロットから出した。空きの保管スロット：$(ssh "${SSHO[@]}" lab@$SRV "sudo mtx -f $CH status" | grep -c 'Storage Element [0-9]*:Empty')"
