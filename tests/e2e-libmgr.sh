#!/bin/bash
# テープライブラリの管理 API（libmgr：REST・SNMP・トラップ）を、冗長構成（fc-tape-ha）の上で通しで確かめる。
# 監視する側（このホスト・監視局 mon）と、テープを使う側（サーバ srv1 が FC 越しに SCSI で操作）の両方から見る。
#
#   tests/e2e-libmgr.sh            いま動いている構成で
#   tests/e2e-libmgr.sh --deploy   作り直してから
#
# 確かめること：
#   1. 両方のライブラリの REST と SNMP が答え、中身が一致する（シリアル番号・状態・ドライブとスロットの数）
#   2. サーバがテープを入れる・出すと、REST・SNMP のドライブの状態が変わり、トラップ（fsDriveLoad・fsDriveUnload）が監視局に届く
#   3. API でオフラインにすると、サーバからのロボットの命令が通らなくなり、SNMP の状態が offline になってトラップが届く。戻すと戻る
#   4. API で TapeAlert を立てると、SNMP とトラップに出て、サーバが TapeAlert のログで読める。消すと消える
#   5. API でテープを投入すると、メールスロットに入り（トラップ fsIEImport）、サーバから見える。サーバが保管スロットへ移すと fsIERemove
set -uo pipefail
cd "$(dirname "$0")/.."
if [ "${1:-}" = --deploy ]; then
  echo "== 作り直す"
  sudo docker unpause clab-fctape-ha-fab-a >/dev/null 2>&1 || true
  sudo containerlab destroy -t lab/clab/run-ha.clab.yml --cleanup >/dev/null 2>&1 || true
  lab/clab/prepare.sh ha >/dev/null || { echo "prepare に失敗" >&2; exit 1; }
  sudo containerlab deploy -t lab/clab/run-ha.clab.yml >/dev/null || { echo "deploy に失敗" >&2; exit 1; }
fi
SRV=172.30.31.21
LIBS=(172.30.31.22 172.30.31.23)
MON=clab-fctape-ha-mon
E=.1.3.6.1.4.1.99999.1
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
srv() { ssh "${SSHO[@]}" lab@$SRV "$@"; }
FAILS=0
ok() { echo "  OK  $*"; }
ng() { echo "  NG  $*"; FAILS=$((FAILS+1)); }
step() { echo "== $*"; }
wait_for() {
  local t=$1 what=$2; shift 2
  local end=$((SECONDS+t))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$what（$((t-(end-SECONDS))) 秒）"; return 0; fi
    sleep 1
  done
  ng "$what（$t 秒待っても成り立たない）"; return 1
}
rest() { curl -s -m 5 "http://$1:8080/api/v1/$2"; }
jq_() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
snmp() { snmpget -v2c -c public -On -Oqv "$1" "$2" 2>/dev/null | tr -d '"'; }
MARK=$(date -u +%Y-%m-%dT%H:%M:%SZ)
traps() { sudo docker logs --since "$MARK" $MON 2>&1 | grep '^TRAP'; }
# トラップが届いたか：trap_has <通知の番号> <含むはずの文字>
trap_has() { traps | grep -F "OID: $E.0.$1|" | grep -q -F "$2"; }
# サーバから見たライブラリ 1 のロボット（経路 A）
robot() { srv "for s in \$(lsscsi -g | awk '/mediumx/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw $1 && { echo \$s; break; }; done"; }

step "0. 管理 API が起きるのを待つ"
for ip in "${LIBS[@]}"; do
  wait_for 300 "$ip の REST が答える" rest $ip library
done

step "1. REST と SNMP"
for ip in "${LIBS[@]}"; do
  L=$(rest $ip library)
  ser=$(jq_ 'd["library"]["serial"]' <<<"$L"); st=$(jq_ 'd["library"]["status"]' <<<"$L")
  nd=$(jq_ 'd["library"]["driveCount"]' <<<"$L"); ns=$(jq_ 'd["library"]["slotCount"]' <<<"$L"); ni=$(jq_ 'd["library"]["ieSlotCount"]' <<<"$L")
  s=$(snmp $ip $E.1.3.0); s4=$(snmp $ip $E.1.4.0); s5=$(snmp $ip $E.1.5.0); s6=$(snmp $ip $E.1.6.0); s7=$(snmp $ip $E.1.7.0)
  name=$(snmp $ip .1.3.6.1.2.1.1.5.0)
  if [ "$st" = online ] && [ "$s" = "$ser" ] && [ "$s4" = 1 ] && [ "$s5" = "$nd" ] && [ "$s6" = "$ns" ] && [ "$s7" = "$ni" ]; then
    ok "$ip（$name）：$ser、online、ドライブ $nd・保管 $ns・メール $ni（REST と SNMP が一致）"
  else
    ng "$ip：REST（$ser $st $nd $ns $ni）と SNMP（$s $s4 $s5 $s6 $s7）が合わない"
  fi
  rows=$(snmpwalk -v2c -c public -On $ip $E.2.1.2 2>/dev/null | wc -l)
  [ "$rows" = "$nd" ] && ok "  SNMP のドライブの表が $rows 行" || ng "  SNMP のドライブの表が $rows 行（$nd のはず）"
done

step "2. サーバがテープを入れる・出す（ライブラリ 1）"
IP=${LIBS[0]}
CH=$(robot LIB1_A)
SLOT=$(srv "sudo mtx -f $CH status" | awk -F'[ :]+' '/Storage Element [0-9]+:Full/ && !/IMPORT/ {print $4; exit}')
TAG=$(srv "sudo mtx -f $CH status" | awk -v s="$SLOT" -F'VolumeTag=' '$0 ~ "Storage Element "s":Full" {gsub(/ /,"",$2); print $2; exit}')
srv "sudo mtx -f $CH load $SLOT 1" >/dev/null && ok "サーバがスロット $SLOT のテープ $TAG をドライブ 2 に入れた" || ng "サーバがテープを入れられない"
loaded() { [ "$(snmp $IP $E.2.1.4.2)" = 2 ] && [ "$(snmp $IP $E.2.1.5.2)" = "$TAG" ]; }
wait_for 10 "SNMP：ドライブ 2 が loaded、テープ $TAG" loaded
[ "$(rest $IP drives | jq_ '[x for x in d if x["index"]==2][0]["volume"]')" = "$TAG" ] && ok "REST：ドライブ 2 のテープ $TAG" || ng "REST のドライブ 2 が合わない"
wait_for 10 "トラップ fsDriveLoad（$TAG）" trap_has 2 "$TAG"
srv "sudo mtx -f $CH unload $SLOT 1" >/dev/null
empty2() { [ "$(snmp $IP $E.2.1.4.2)" = 1 ]; }
wait_for 10 "出すと SNMP：ドライブ 2 が empty" empty2
wait_for 10 "トラップ fsDriveUnload（$TAG）" trap_has 3 "$TAG"

step "3. オフライン・オンライン（ライブラリ 1）"
curl -s -m 10 -X POST http://$IP:8080/api/v1/library/offline >/dev/null
# SNMP の答えは libmgr の状態を 1 秒までためるので、数秒以内に変わればよい
st_is() { [ "$(snmp $IP $E.1.4.0)" = "$1" ]; }
wait_for 5 "SNMP：fsLibStatus = offline(2)" st_is 2
srv "sudo mtx -f $CH status" >/dev/null 2>&1 && ng "オフラインなのにサーバからロボットの命令が通った" || ok "サーバからのロボットの命令が通らない（Not Ready）"
wait_for 10 "トラップ fsLibStatusChange（offline）" trap_has 1 "INTEGER: 2"
curl -s -m 10 -X POST http://$IP:8080/api/v1/library/online >/dev/null
wait_for 5 "戻すと SNMP：online(1)" st_is 1
srv "sudo mtx -f $CH status" >/dev/null 2>&1 && ok "サーバからのロボットの命令が通る" || ng "オンラインに戻してもサーバから命令が通らない"
wait_for 10 "トラップ fsLibStatusChange（online）" trap_has 1 "INTEGER: 1"

step "4. TapeAlert（ライブラリ 1 のドライブ 2、フラグ 15＝カートリッジのメモリの故障）"
curl -s -m 10 -X PUT http://$IP:8080/api/v1/drives/2/tapealert -d '{"flags":[15]}' >/dev/null
al_on() { [ "$(snmp $IP $E.2.1.6.2)" = 15 ] && [ "$(snmp $IP $E.1.8.0)" = 1 ]; }
wait_for 5 "SNMP：fsDriveAlerts.2 = 15、fsLibAlertDrives = 1" al_on
wait_for 10 "トラップ fsDriveAlert（15）" trap_has 4 'STRING: "15"'
DRV=$(srv "for s in \$(lsscsi -g | awk '/tape/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw LIB1_A2 && { echo \$s; break; }; done")
srv "sudo sg_logs -p 0x2e $DRV" | grep -q "Memory chip in cartridge failure: 1" \
  && ok "サーバが TapeAlert のログで読めた（$DRV：Memory chip in cartridge failure）" || ng "サーバの TapeAlert のログにフラグが出ない"
curl -s -m 10 -X PUT http://$IP:8080/api/v1/drives/2/tapealert -d '{"flags":[]}' >/dev/null
al_off() { [ "$(snmp $IP $E.1.8.0)" = 0 ]; }
wait_for 5 "消すと fsLibAlertDrives = 0" al_off

step "5. テープの投入（ライブラリ 1）"
BC=$(printf 'I%05dL8' $((RANDOM % 100000)))
R=$(curl -s -m 30 -X POST http://$IP:8080/api/v1/import -d "{\"barcode\":\"$BC\"}")
IE=$(jq_ "[s['address'] for s in d if s.get('volume')=='$BC'][0]" <<<"$R" 2>/dev/null)
[ -n "$IE" ] && ok "REST：テープ $BC がメールスロット $IE に入った" || { ng "投入できない：$R"; IE=0; }
wait_for 10 "トラップ fsIEImport（$BC）" trap_has 5 "$BC"
srv "sudo mtx -f $CH status" | grep -q "Storage Element $IE IMPORT/EXPORT:Full :VolumeTag=$BC" && ok "サーバから $BC がメールスロット $IE に見える" || ng "サーバから投入したテープが見えない"
DEST=$(srv "sudo mtx -f $CH status" | awk -F'[ :]+' '/Storage Element [0-9]+:Empty/ && !/IMPORT/ {print $4; exit}')
srv "sudo mtx -f $CH transfer $IE $DEST" >/dev/null && ok "サーバがメールスロット $IE から保管スロット $DEST へ移した" || ng "サーバが移せない"
wait_for 10 "トラップ fsIERemove（$BC）" trap_has 6 "$BC"
in_dest() { [ "$(snmp $IP $E.3.1.4.$DEST)" = "$BC" ]; }
wait_for 5 "SNMP：保管スロット $DEST に $BC" in_dest

step "6. ライブラリ 2 のトラップ"
IP2=${LIBS[1]}
curl -s -m 10 -X PUT http://$IP2:8080/api/v1/drives/1/tapealert -d '{"flags":[20]}' >/dev/null
wait_for 10 "ライブラリ 2 から fsDriveAlert（シリアル LIB2_A）" trap_has 4 'STRING: "LIB2_A"'
curl -s -m 10 -X PUT http://$IP2:8080/api/v1/drives/1/tapealert -d '{"flags":[]}' >/dev/null

echo
echo "  届いたトラップ：$(traps | wc -l) 件"
echo
if [ $FAILS = 0 ]; then echo "全部 OK"; else echo "NG が $FAILS 件"; fi
exit $((FAILS > 0))
