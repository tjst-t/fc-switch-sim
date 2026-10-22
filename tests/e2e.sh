#!/bin/bash
# ContainerLab の構成（lab/clab/fc-tape.clab.yml）を通しで確かめる。
#
#   tests/e2e.sh            いま動いている構成を確かめる
#   tests/e2e.sh --deploy   作り直して（prepare → deploy）から確かめる
#   tests/e2e.sh --deploy --destroy   終わったら壊す
#
# 確かめること：
#   1. 両方の VM がファブリックにログインし、fc_host に FC_ID が出る
#   2. サーバにロボットとドライブ 4 台が FC 越しに見える
#   3. テープの読み書き：ブロック 256KB・1MB・2MB で書いて読み戻し、中身が一致する
#   4. ゾーンから外すと装置が消え、戻すと戻る
#   5. スイッチのポートを無効にすると装置が消え、有効にすると戻る
#   6. BMC（Redfish）が答え、VM の電源が On
set -uo pipefail
cd "$(dirname "$0")/.."
DEPLOY=0; DESTROY=0
for a in "$@"; do
  case "$a" in
    --deploy) DEPLOY=1 ;;
    --destroy) DESTROY=1 ;;
    *) echo "知らない引数 $a" >&2; exit 2 ;;
  esac
done

API=http://172.30.30.10:8080/api/v1
SRV=172.30.30.21; LIB=172.30.30.22
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
srv() { ssh "${SSHO[@]}" lab@$SRV "$@"; }
lib() { ssh "${SSHO[@]}" lab@$LIB "$@"; }
FAILS=0
ok() { echo "  OK  $*"; }
ng() { echo "  NG  $*"; FAILS=$((FAILS+1)); }
step() { echo "== $*"; }
# 条件が成り立つまで待つ：wait_for 秒 説明 命令...
wait_for() {
  local t=$1 what=$2; shift 2
  local end=$((SECONDS+t))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$what（$((t-(end-SECONDS))) 秒）"; return 0; fi
    sleep 2
  done
  ng "$what（$t 秒待っても成り立たない）"; diag; return 1
}
# NG のときの診断：両方の VM の経路の状態とカーネルのログの末尾、スイッチの最近の出来事
diag() {
  for h in srv lib; do
    echo "      --- $h"
    $h 'cat /sys/class/fc_host/host*/port_state; for r in /sys/class/fc_remote_ports/rport-*; do echo "$(cat $r/port_name $r/port_id $r/port_state | tr "\n" " ")"; done; sudo dmesg | grep -v -E "exch|Missing Disc" | tail -15' 2>&1 | sed 's/^/      /'
  done
  echo "      --- スイッチ"
  curl -s -m 5 "$API/events?n=12" | grep -E '"(time|message)"' | paste - - | sed 's/^ */      /'
  echo "      --- ネームサーバ"
  curl -s -m 5 "$API/nameserver" | grep -E '"(fcid|wwpn|rscnRegistered)"' | paste - - - | sed 's/^ */      /'
  echo "      --- ゾーニング"
  curl -s -m 5 "$API/zoning" | tr -d '\n ' | sed 's/^/      /'; echo
  echo "      --- サーバの dmesg（RSCN・disc・rport 010100）"
  srv 'sudo dmesg | grep -E "disc:|RSCN|rport 010100|GPN" | grep -v exch | tail -40' 2>&1 | sed 's/^/      /'
}
# grep -c は 0 件のとき失敗で終わるので、; true で終わり方をそろえる
devcount() { srv 'lsscsi | grep -c -E "tape|mediumx"; true' 2>/dev/null; }
devs_are() { [ "$(devcount)" = "$1" ]; }
ZONE_ON='{"defaultZone":"noaccess","aliases":{"srv1":["20:00:52:54:00:fc:00:01"],"lib1":["20:00:52:54:00:fc:00:02"]},"zones":[{"name":"z_srv1_lib1","members":["srv1","lib1"]}]}'
ZONE_OFF='{"defaultZone":"noaccess","zones":[{"name":"z_other","members":["20:00:00:00:00:00:00:99","20:00:52:54:00:fc:00:02"]}]}'

if [ $DEPLOY = 1 ]; then
  step "作り直す"
  sudo containerlab destroy -t lab/clab/run.clab.yml --cleanup >/dev/null 2>&1 || true
  lab/clab/prepare.sh >/dev/null || { echo "prepare に失敗" >&2; exit 1; }
  sudo containerlab deploy -t lab/clab/run.clab.yml >/dev/null || { echo "deploy に失敗" >&2; exit 1; }
fi

step "0. 決まった状態に戻す（ゾーン z_srv1_lib1、ポートは全部有効）"
api_up() { curl -s -m 3 -o /dev/null $API/switch; }
wait_for 120 "スイッチの API が答える" api_up
if [ "${E2E_NO_RESET:-}" = 1 ]; then
  echo "  （E2E_NO_RESET=1：ゾーンを入れ直さない。調査用）"
else
  curl -s -m 5 -X PUT $API/zoning -d "$ZONE_ON" >/dev/null && ok "ゾーンを戻した" || ng "ゾーンを戻せない"
fi
for i in 0 1; do curl -s -m 5 -X POST $API/ports/$i/enable >/dev/null; done

step "1. ログイン"
wait_for 300 "サーバの設定が終わった" srv 'test -f /var/log/fcsim-setup.rc'
wait_for 300 "ライブラリの設定が終わった" lib 'test -f /var/log/fcsim-setup.rc'
[ "$(srv cat /var/log/fcsim-setup.rc)" = 0 ] && ok "サーバの設定が成功" || ng "サーバの設定が失敗（/var/log/fcsim-setup.log）"
[ "$(lib cat /var/log/fcsim-setup.rc)" = 0 ] && ok "ライブラリの設定が成功" || ng "ライブラリの設定が失敗（/var/log/fcsim-setup.log）"
nsn() { [ "$(curl -s -m 5 $API/nameserver | grep -c '"fcid"')" = 2 ]; }
wait_for 120 "ネームサーバに 2 件" nsn
s=$(srv 'cat /sys/class/fc_host/host*/port_id /sys/class/fc_host/host*/port_state | tr "\n" " "')
[[ "$s" == "0x010000 Online "* ]] && ok "サーバの fc_host：$s" || ng "サーバの fc_host：$s"

# 診断のために libfc・libfcoe のデバッグのログを出す
for h in srv lib; do $h 'echo 0x3f | sudo tee /sys/module/libfc/parameters/debug_logging >/dev/null; echo 0xff | sudo tee /sys/module/libfcoe/parameters/debug_logging >/dev/null' 2>/dev/null; done

step "2. 装置が見える"
wait_for 60 "ロボットとドライブ 4 台（5 個）" devs_are 5
# 以降の時間を縮める：経路が切れてから装置を消すまで 10 秒
srv 'for r in /sys/class/fc_remote_ports/rport-*; do echo 10 | sudo tee $r/dev_loss_tmo >/dev/null 2>&1; done'

step "3. テープの読み書き"
out=$(srv 'set -e
  CH=$(lsscsi -g | awk "/mediumx/ {print \$NF; exit}")
  T=""
  for s in $(lsscsi -g | awk "/tape/ {print \$NF}"); do
    if sudo sg_vpd -p sn $s | grep -q XYZZY_A1; then T=/dev/n$(lsscsi -g | awk -v s=$s "\$NF==s {print \$(NF-1)}" | sed "s#/dev/##"); fi
  done
  SLOT=$(sudo mtx -f $CH status | awk -F"[ :]+" "/Storage Element [0-9]+:Full/ && !/IMPORT/ {print \$4; exit}")
  cd /var/tmp
  sudo mtx -f $CH load $SLOT 0 >/dev/null
  sudo mt -f $T rewind
  for bs in 256k 1M 2M; do sudo head -c 64000000 /dev/urandom | sudo tee w$bs.bin >/dev/null; done
  for bs in 256k 1M 2M; do sudo timeout 60 dd if=w$bs.bin of=$T bs=$bs status=none; done
  sudo mt -f $T rewind
  for bs in 256k 1M 2M; do sudo timeout 60 dd if=$T of=r$bs.bin bs=$bs status=none; done
  for bs in 256k 1M 2M; do
    if sudo cmp -s w$bs.bin r$bs.bin; then echo "MATCH $bs"; else echo "DIFFER $bs"; fi
  done
  sudo mt -f $T rewind
  sudo mtx -f $CH unload $SLOT 0 >/dev/null
  sudo rm -f w*.bin r*.bin
  echo "DONE slot=$SLOT drive=$T"' 2>&1)
for bs in 256k 1M 2M; do
  grep -q "MATCH $bs" <<<"$out" && ok "ブロック $bs：64MB を書いて読み戻し、一致" || ng "ブロック $bs：一致しない"
done
grep -q DONE <<<"$out" && ok "テープを出し入れした（$(grep DONE <<<"$out" | cut -d' ' -f2-)）" || { ng "テープの操作が途中で止まった"; echo "$out" | tail -5 | sed 's/^/      /'; }

step "4. ゾーニング"
curl -s -m 5 -X PUT $API/zoning -d "$ZONE_OFF" >/dev/null
wait_for 60 "ゾーンから外すと装置が消える" devs_are 0
curl -s -m 5 -X PUT $API/zoning -d "$ZONE_ON" >/dev/null
wait_for 60 "ゾーンを戻すと装置が戻る" devs_are 5
srv 'for r in /sys/class/fc_remote_ports/rport-*; do echo 10 | sudo tee $r/dev_loss_tmo >/dev/null 2>&1; done'

step "5. ポートの無効化"
srv 'for r in /sys/class/fc_remote_ports/rport-*; do echo 10 | sudo tee $r/dev_loss_tmo >/dev/null 2>&1; done'
curl -s -m 5 -X POST $API/ports/1/disable >/dev/null
wait_for 60 "ライブラリのポートを無効にすると装置が消える" devs_are 0
curl -s -m 5 -X POST $API/ports/1/enable >/dev/null
wait_for 90 "有効に戻すと装置が戻る" devs_are 5

step "6. BMC"
p=$(curl -sk -m 5 -u admin:password https://$SRV/redfish/v1/Systems/1 | grep -o '"PowerState":"[A-Za-z]*"')
[ "$p" = '"PowerState":"On"' ] && ok "Redfish：サーバの $p" || ng "Redfish：サーバの電源の状態が読めない（$p）"

step "スイッチの数え"
curl -s -m 5 $API/ports | grep -E '"(kernelDrops|crcErrors|txErrors)"' | tr -d ' ,' | paste -sd' ' | sed 's/^/  /'

if [ $DESTROY = 1 ]; then
  step "壊す"
  sudo containerlab destroy -t lab/clab/run.clab.yml --cleanup >/dev/null 2>&1
fi
echo
if [ $FAILS = 0 ]; then echo "全部 OK"; else echo "NG が $FAILS 件"; fi
exit $((FAILS > 0))
