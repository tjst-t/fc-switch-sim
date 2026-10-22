#!/bin/bash
# Zabbix でテープライブラリ（SNMP・REST）と FC スイッチ（REST）を監視できるか、障害を起こして確かめる。
# 前提：冗長構成（fctape-ha）が動いていて、lab/clab/zabbix.sh up と tests/zabbix-setup.py が済んでいる
# （--setup を付けると、この 2 つも流す）。
#
# 確かめること（どれも Zabbix の「障害」に上がり、戻すと消える）：
#   1. 値が取れている：lib1・lib2 の SNMP（状態・ドライブ 4 台の発見）と REST、fab-a・fab-b のポート 4 つの発見
#   2. ライブラリ 1 をオフライン → 「not online（SNMP）」「not online（REST）」
#   3. ライブラリ 2 のドライブ 3 に TapeAlert → 「Drive 3 ... TapeAlert 20」
#   4. ライブラリ 1 のメールスロットにテープを投入 → 「cartridge waiting in I/E slot」
#   5. スイッチ A のポート 1（lib1）を無効 → 「port 1 (eth2) is Disabled」
#   6. スイッチ B を止める（docker pause）→ 「FC switch fab-b: REST not responding」（1 分のデータ無し）
#   7. サーバがテープを入れると、Zabbix のドライブの値（state・volume）が変わる
#   8. SNMP トラップ（Zabbix のトラップ受信 .43 で受ける）：オフラインのトラップで「went offline (SNMP trap)」が立ち、
#      オンラインのトラップで消える。ドライブ 1 の TapeAlert のトラップで「Drive 1 ... TapeAlert raised (SNMP trap)」が立ち、
#      そのドライブにテープを出し入れしても（そのトラップが来ても）立ったまま、フラグを消すトラップで消える。
#      知らないトラップは snmptrap.fallback に入る
#   9. スイッチの SNMP とトラップ：SNMP でログインの数・ポートを発見。スイッチ A のポート 2（lib2）を無効にすると
#      「port 2 (eth3) went down (SNMP trap)」と「not online (SNMP)」が立ち、有効に戻すと消える。ログイン・ログアウトのトラップに
#      lib2 の WWPN が入る。スイッチ B のゾーニングを入れ直すと「zoning changed (SNMP trap)」が立ち、fswZoningChanges が増える
set -uo pipefail
cd "$(dirname "$0")/.."
if [ "${1:-}" = --setup ]; then
  lab/clab/zabbix.sh up && python3 tests/zabbix-setup.py || exit 1
  echo "（値が集まるのを 90 秒待つ）"; sleep 90
fi
FAILS=0
ok() { echo "  OK  $*"; }
ng() { echo "  NG  $*"; FAILS=$((FAILS+1)); }
step() { echo "== $*"; }
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
srv() { ssh "${SSHO[@]}" lab@172.30.31.21 "$@"; }
LIB1=http://172.30.31.22:8080/api/v1
LIB2=http://172.30.31.23:8080/api/v1
SWA=http://172.30.31.10:8080/api/v1
# Zabbix に聞く：zq problems | zq value <host> <key>
zq() {
  python3 - "$@" <<'PY'
import sys, importlib.util
spec = importlib.util.spec_from_file_location("zs", "tests/zabbix-setup.py"); zs = importlib.util.module_from_spec(spec); spec.loader.exec_module(zs)
z = zs.Zbx("http://172.30.31.42:8080/api_jsonrpc.php")
if sys.argv[1] == "problems":
    for p in z.call("problem.get", {"output": ["name"]}):
        print(p["name"])
elif sys.argv[1] == "value":
    h = z.call("host.get", {"filter": {"host": [sys.argv[2]]}})[0]["hostid"]
    it = z.call("item.get", {"hostids": h, "filter": {"key_": sys.argv[3]}, "output": ["lastvalue", "state", "error"]})
    print(it[0]["lastvalue"] if it and it[0]["state"] == "0" else "ERR " + (it[0]["error"] if it else "no item"))
elif sys.argv[1] == "count":
    h = z.call("host.get", {"filter": {"host": [sys.argv[2]]}})[0]["hostid"]
    print(len(z.call("item.get", {"hostids": h, "search": {"key_": sys.argv[3]}, "output": ["itemid"], "filter": {"state": "0"}})))
PY
}
has_problem() { zq problems | grep -q -F "$1"; }
no_problem() { ! zq problems | grep -q -F "$1"; }
value_is() { [ "$(zq value "$1" "$2")" = "$3" ]; }
wait_for() {
  local t=$1 what=$2; shift 2
  local end=$((SECONDS+t))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$what（$((t-(end-SECONDS))) 秒）"; return 0; fi
    sleep 5
  done
  ng "$what（$t 秒待っても成り立たない）"; return 1
}

tests/lib-cleanup.sh >/dev/null 2>&1 || true   # 前の試験で投入したテープが残っていれば出す（保管スロットがあふれないように）
step "1. 値が取れている"
for h in lib1 lib2; do
  wait_for 90 "$h：SNMP の状態 = 1（online）" value_is $h fcsim.lib.status 1
  wait_for 60 "$h：REST の状態 = online" value_is $h fcsim.rest.status online
  four() { [ "$(zq count $1 fcsim.drive.state)" = 4 ]; }
  wait_for 120 "$h：SNMP でドライブ 4 台を発見" four $h
  wait_for 60 "$h：メールスロットのテープ = 0（REST）" value_is $h fcsim.rest.iefull 0
done
for h in fab-a fab-b; do
  ports4() { [ "$(zq count $1 fcsim.sw.port.state)" = 4 ]; }
  wait_for 60 "$h：REST でポート 4 つを発見" ports4 $h
  wait_for 30 "$h：ログイン 4" value_is $h fcsim.sw.logins 4
done
none() { [ -z "$(zq problems | grep -v -E "unmatched SNMP trap|zoning changed")" ]; }   # お知らせ（10 分で消える）は除く
wait_for 120 "障害は 0 件" none || zq problems | sed 's/^/      /'

step "2. ライブラリ 1 をオフライン"
curl -s -m 10 -X POST $LIB1/library/offline >/dev/null
wait_for 90 "障害：Tape library lib1 is not online (SNMP)" has_problem "Tape library lib1 is not online (SNMP)"
wait_for 60 "障害：Tape library lib1 is not online (REST)" has_problem "Tape library lib1 is not online (REST)"
curl -s -m 10 -X POST $LIB1/library/online >/dev/null
wait_for 90 "戻すと消える" no_problem "Tape library lib1 is not online"

step "3. ライブラリ 2 のドライブ 3 に TapeAlert 20（クリーニングが要る）"
curl -s -m 10 -X PUT $LIB2/drives/3/tapealert -d '{"flags":[20]}' >/dev/null
wait_for 90 "障害：Drive 3 (LIB2_A3) on lib2: TapeAlert 20" has_problem "Drive 3 (LIB2_A3) on lib2: TapeAlert 20"
curl -s -m 10 -X PUT $LIB2/drives/3/tapealert -d '{"flags":[]}' >/dev/null
wait_for 90 "消すと消える" no_problem "on lib2: TapeAlert"

step "4. ライブラリ 1 のメールスロットにテープを投入"
BC=$(printf 'Z%05dL8' $((RANDOM % 100000)))
IE=$(curl -s -m 30 -X POST $LIB1/import -d "{\"barcode\":\"$BC\"}" | python3 -c "import json,sys; print([s['address'] for s in json.load(sys.stdin) if s.get('volume')=='$BC'][0])" 2>/dev/null)
[ -n "$IE" ] && ok "テープ $BC をメールスロット $IE に投入した" || ng "投入できない"
wait_for 90 "障害：cartridge waiting in I/E slot" has_problem "Tape library lib1: cartridge waiting in I/E slot"
CH=$(srv "for s in \$(lsscsi -g | awk '/mediumx/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw LIB1_A && { echo \$s; break; }; done")
DEST=$(srv "sudo mtx -f $CH status" | awk -F'[ :]+' '/Storage Element [0-9]+:Empty/ && !/IMPORT/ {print $4; exit}')
srv "sudo mtx -f $CH transfer $IE $DEST" >/dev/null && ok "サーバが保管スロット $DEST へ移した" || ng "サーバが移せない"
wait_for 90 "移すと消える" no_problem "cartridge waiting in I/E slot"

step "5. スイッチ A のポート 1（lib1）を無効"
curl -s -m 10 -X POST $SWA/ports/1/disable >/dev/null
wait_for 90 "障害：FC switch fab-a port 1 (eth2) is Disabled" has_problem "FC switch fab-a port 1 (eth2) is Disabled"
curl -s -m 10 -X POST $SWA/ports/1/enable >/dev/null
wait_for 120 "有効に戻すと消える" no_problem "FC switch fab-a port 1"

step "6. スイッチ B を止める（docker pause）"
sudo docker pause clab-fctape-ha-fab-b >/dev/null
wait_for 150 "障害：FC switch fab-b: REST not responding" has_problem "FC switch fab-b: REST not responding"
sudo docker unpause clab-fctape-ha-fab-b >/dev/null
wait_for 120 "戻すと消える" no_problem "FC switch fab-b: REST not responding"

step "7. サーバがテープを入れると、Zabbix のドライブの値が変わる"
SLOT=$(srv "sudo mtx -f $CH status" | awk -F'[ :]+' '/Storage Element [0-9]+:Full/ && !/IMPORT/ {print $4; exit}')
TAG=$(srv "sudo mtx -f $CH status" | awk -v s="$SLOT" -F'VolumeTag=' '$0 ~ "Storage Element "s":Full" {gsub(/ /,"",$2); print $2; exit}')
srv "sudo mtx -f $CH load $SLOT 0" >/dev/null && ok "サーバがテープ $TAG をドライブ 1 に入れた" || ng "サーバがテープを入れられない"
wait_for 60 "Zabbix：lib1 のドライブ 1 の volume = $TAG" value_is lib1 "fcsim.drive.volume[1]" "$TAG"
wait_for 30 "Zabbix：lib1 のドライブ 1 の state = 2（loaded）" value_is lib1 "fcsim.drive.state[1]" 2
srv "sudo mtx -f $CH unload $SLOT 0" >/dev/null
wait_for 60 "出すと state = 1（empty）" value_is lib1 "fcsim.drive.state[1]" 1

step "8. SNMP トラップ"
has_trap_problem() { has_problem "$1" && zq problems | grep -F "$1" | grep -q "SNMP trap"; }
curl -s -m 10 -X POST $LIB1/library/offline >/dev/null
wait_for 60 "障害：Tape library lib1 went offline (SNMP trap)" has_problem "Tape library lib1 went offline (SNMP trap)"
curl -s -m 10 -X POST $LIB1/library/online >/dev/null
wait_for 60 "online のトラップで消える" no_problem "went offline (SNMP trap)"
curl -s -m 10 -X PUT $LIB1/drives/1/tapealert -d '{"flags":[15]}' >/dev/null
wait_for 60 "障害：Drive 1 (LIB1_A1) on lib1: TapeAlert raised (SNMP trap)" has_problem "Drive 1 (LIB1_A1) on lib1: TapeAlert raised (SNMP trap)"
srv "sudo mtx -f $CH load $SLOT 0 && sleep 5 && sudo mtx -f $CH unload $SLOT 0" >/dev/null && ok "サーバがドライブ 1 にテープを出し入れした（fsDriveLoad・fsDriveUnload のトラップ）" || ng "テープの出し入れに失敗"
sleep 15
has_problem "Drive 1 (LIB1_A1) on lib1: TapeAlert raised (SNMP trap)" && ok "出し入れのトラップでは障害は消えない" || ng "出し入れのトラップで障害が消えた"
lv() { zq value lib1 'snmptrap["99999\.1\.0\.[2356]\s"]' | grep -q "99999.1.0.3"; }
wait_for 30 "出し入れのトラップが Zabbix の項目に入った（最後は fsDriveUnload）" lv
curl -s -m 10 -X PUT $LIB1/drives/1/tapealert -d '{"flags":[]}' >/dev/null
wait_for 60 "フラグを消すトラップで消える" no_problem "TapeAlert raised (SNMP trap)"
# 知らないトラップ（ライブラリの VM から、知らない通知の番号で送る）
ssh "${SSHO[@]}" lab@172.30.31.22 "snmptrap -v2c -c public 172.30.31.43:1162 '' .1.3.6.1.4.1.99999.1.0.99 .1.3.6.1.4.1.99999.1.1.3.0 s LIB1_A" \
  && ok "ライブラリ 1 から知らないトラップ（.0.99）を送った" || ng "知らないトラップを送れない"
fb() { zq value lib1 snmptrap.fallback | grep -q "99999.1.0.99"; }
wait_for 30 "snmptrap.fallback に入った" fb
wait_for 30 "障害：unmatched SNMP trap received" has_problem "Tape library lib1: unmatched SNMP trap received"
echo "  （unmatched の障害は 10 分で消える。試験では待たない）"

step "9. スイッチの SNMP とトラップ"
SWB=http://172.30.31.11:8080/api/v1
for h in fab-a fab-b; do
  wait_for 60 "$h：SNMP のログインの数 = 4" value_is $h fcsim.sw.snmp.logins 4
  ports_snmp() { [ "$(zq count $1 fcsim.sw.snmp.state)" = 4 ]; }
  wait_for 120 "$h：SNMP でポート 4 つを発見" ports_snmp $h
done
traps_ports() { [ "$(zq count fab-a 'snmptrap["99999\.2\.2\.1\.1\.')" = 4 ]; }
wait_for 120 "fab-a：ポートごとのトラップの項目が 4 つ" traps_ports
curl -s -m 10 -X POST $SWA/ports/2/disable >/dev/null
wait_for 60 "障害：FC switch fab-a port 2 (eth3) went down (SNMP trap)" has_problem "FC switch fab-a port 2 (eth3) went down (SNMP trap)"
wait_for 60 "障害：FC switch fab-a port 2 (eth3) not online (SNMP)" has_problem "FC switch fab-a port 2 (eth3) not online (SNMP)"
lo() { zq value fab-a 'snmptrap["99999\.2\.0\.[23]\s"]' | grep -q "99999.2.0.3" && zq value fab-a 'snmptrap["99999\.2\.0\.[23]\s"]' | grep -q "20:00:52:54:00:fc:01:03"; }
wait_for 30 "ログアウトのトラップ（lib2 の WWPN 20:00:52:54:00:fc:01:03）が入った" lo
curl -s -m 10 -X POST $SWA/ports/2/enable >/dev/null
wait_for 120 "有効に戻すと went down (SNMP trap) が消える" no_problem "port 2 (eth3) went down (SNMP trap)"
wait_for 60 "not online (SNMP) も消える" no_problem "port 2 (eth3) not online (SNMP)"
li() { zq value fab-a 'snmptrap["99999\.2\.0\.[23]\s"]' | grep -q "99999.2.0.2"; }
wait_for 30 "ログインのトラップが入った" li
before=$(zq value fab-b fcsim.sw.snmp.zoningchanges)
curl -s -m 10 -X PUT $SWB/zoning -d "$(curl -s -m 5 $SWB/zoning)" >/dev/null
wait_for 60 "障害：FC switch fab-b: zoning changed (SNMP trap)" has_problem "FC switch fab-b: zoning changed (SNMP trap)"
zc() { [ "$(zq value fab-b fcsim.sw.snmp.zoningchanges)" -gt "${before:-0}" ] 2>/dev/null; }
wait_for 60 "SNMP の fswZoningChanges が増えた（$before から）" zc
echo "  （zoning changed の障害は 10 分で消える。試験では待たない）"

echo
p=$(zq problems | grep -v -E "unmatched SNMP trap|zoning changed"); [ -z "$p" ] && echo "  終わりの障害：0 件" || { echo "  終わりに残った障害："; echo "$p" | sed 's/^/    /'; }
echo
tests/lib-cleanup.sh | sed "s/^/  後始末：/"
if [ $FAILS = 0 ]; then echo "全部 OK"; else echo "NG が $FAILS 件"; fi
exit $((FAILS > 0))
