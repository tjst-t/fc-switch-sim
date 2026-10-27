#!/bin/bash
# 試験用の口 /_sim/v1（libmgr）で、テープライブラリのチェンジャーに障害・扉・UNIT ATTENTION を入れ、
# サーバ srv1 から FC 越しに SCSI で見える答えを確かめる（冗長構成 fc-tape-ha のライブラリ 1）。
#
#   tests/e2e-sim-faults.sh            いま動いている構成で
#   tests/e2e-sim-faults.sh --deploy   作り直してから
#
# 確かめること：
#   1. MOVE MEDIUM の障害（HARDWARE ERROR 44h/00h）：MOVE MEDIUM と INITIALIZE ELEMENT STATUS（07h・E7h）がその sense で失敗し、
#      READ ELEMENT STATUS は障害の前と同じ在庫を返す。direction=unload なら入れるのは通り、出すのだけ失敗する。消すと戻る
#   2. 扉を開ける：MOVE MEDIUM は NOT READY 04h/03h、TEST UNIT READY・READ ELEMENT STATUS はふつうに答える。
#      notReady にすると TEST UNIT READY も 04h/03h
#   3. 扉を閉じる：次の命令に UNIT ATTENTION 28h/00h が1回だけ出て（見回りに先に取られない）、2回目と別の経路は GOOD。
#      既定で障害も消える
#   4. clearFaults=false・becomingReadySeconds=3：3 秒は NOT READY 04h/01h、そのあと 28h/00h、障害は残る
#   5. この間ずっと libmgr の REST と SNMP の状態は online で、状態の変化の出来事が無く、Zabbix に lib1 の障害が出ない
# 最後に障害と扉を元に戻す（失敗しても戻す）。
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
LIB=172.30.31.22
SER=LIB1_A
API=http://$LIB:8080/api/v1
SIM=http://$LIB:8080/_sim/v1
ZBX=http://172.30.31.42:8080/api_jsonrpc.php
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
jq_() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
sim() { curl -s -m 15 -X "$1" "$SIM/$2" ${3:+-d "$3"}; }
simst() { sim GET state | jq_ "$1"; }
snmp() { snmpget -v2c -c public -On -Oqv "$1" "$2" 2>/dev/null | tr -d '"'; }
# srv1 から sg_raw を送り、答えを "GOOD" か "<key>/<asc>/<ascq>"（16 進）で返す：scsi <sg> <CDB のバイト…>
scsi() {
  local sg=$1; shift
  srv "out=\$(sudo sg_raw -v $sg $* 2>&1 >/dev/null); rc=\$?
       if [ \$rc = 0 ]; then echo GOOD; exit; fi
       echo \"\$out\" | awk '/Raw sense data/ {getline; printf \"%s/%s/%s\n\", \$3, \$13, \$14; f=1; exit} END {if (!f) print \"ERR\"}'"
}
expect() { # expect <期待> <説明> <sg> <CDB…>
  local want=$1 what=$2; shift 2
  local got; got=$(scsi "$@")
  [ "$got" = "$want" ] && ok "$what：$got" || ng "$what：$got（$want のはず）"
}
TUR="00 00 00 00 00 00"
IES="07 00 00 00 00 00"
IESR="E7 00 00 00 00 00 00 00 00 00"
# サーバから見たライブラリ 1 のロボット（経路 2 本）
robots() { srv "for s in \$(lsscsi -g | awk '/mediumx/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw $SER && echo \$s; done"; }
inventory() { srv "sudo mtx -f $CH status" 2>/dev/null | grep -E 'Element'; }
# 要素の住所（ロボットのモードページ 1Dh から）：MOVE MEDIUM の CDB に使う
addr_hex() { printf '%02X %02X' $(($1 >> 8)) $(($1 & 255)); }
move_cdb() { echo "A5 00 00 00 $(addr_hex $1) $(addr_hex $2) 00 00 00 00"; }
# Zabbix（動いていれば）の lib1 の障害
zbx_lib1() {
  python3 - <<'PY' 2>/dev/null
import importlib.util
spec = importlib.util.spec_from_file_location("zs", "tests/zabbix-setup.py"); zs = importlib.util.module_from_spec(spec); spec.loader.exec_module(zs)
z = zs.Zbx("http://172.30.31.42:8080/api_jsonrpc.php")
for p in z.call("problem.get", {"output": ["name"]}):
    if "lib1" in p["name"]:
        print(p["name"])
PY
}
restore() {
  sim DELETE faults/move-medium >/dev/null
  if [ "$(simst 'd["door"]["open"] or d["unitAttentionPending"] or d["becomingReadySeconds"]>0' 2>/dev/null)" != False ]; then
    sim POST door/close '{"clearFaults":true}' >/dev/null
    sleep 1
    for i in $(seq 1 10); do [ "$(scsi $CH $TUR)" = GOOD ] && break; sleep 1; done
  fi
}
trap '[ -n "${CH:-}" ] && restore' EXIT

step "0. 決まった状態に戻す"
wait_for 300 "lib1 の REST が答える" curl -sf -m 3 -o /dev/null $API/library
wait_for 60 "lib1 の /_sim/v1 が答える" curl -sf -m 3 -o /dev/null $SIM/state
mapfile -t CHS < <(robots)
CH=${CHS[0]:-}; CH2=${CHS[1]:-}
[ -n "$CH" ] && [ -n "$CH2" ] && ok "srv1 からライブラリ 1 のロボットが経路 2 本で見える（$CH・$CH2）" || { ng "ロボットが 2 本見えない（${CHS[*]}）"; exit 1; }
restore
# 経路 2 にもたまっている UA（電源投入など、元からあるもの）を消しておく
for c in $CH $CH2; do for i in 1 2 3; do [ "$(scsi $c $TUR)" = GOOD ] && break; done; done
srv "sudo mtx -f $CH status" | grep -q 'Data Transfer Element 0:Empty' || srv "sudo mtx -f $CH unload" >/dev/null 2>&1
[ "$(simst 'd["faults"]["moveMedium"] is None and not d["door"]["open"] and not d["unitAttentionPending"]')" = True ] \
  && ok "障害なし・扉は閉・待っている UA なし" || ng "初めの状態が戻らない：$(sim GET state)"
# 要素の住所：mhVTL の L700 はドライブ 500〜、保管スロット 1000〜
DRV0=500; SLOT1=1000
MARK=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ZBASE=$(zbx_lib1)
[ -z "$ZBASE" ] || echo "      （試験の前から Zabbix に lib1 の障害がある：$ZBASE）"
# 在庫を「番号:テープ」で並べる：mtx（サーバ）と libmgr の REST（見回りの READ ELEMENT STATUS）
inv_mtx() { srv "sudo mtx -f $CH status" 2>/dev/null | python3 -c "import re,sys; print('\\n'.join(sorted(m.group(1)+':'+m.group(2) for m in re.finditer(r'Storage Element (\\d+)(?: IMPORT/EXPORT)?:Full :VolumeTag=(\\S+)', sys.stdin.read()))))"; }
inv_rest() { curl -s -m 5 $API/slots | jq_ "'\\n'.join(sorted(str(x['address'])+':'+x['volume'] for x in d if x['full']))"; }
inv_same() { local a b; a=$(inv_mtx); b=$(inv_rest); [ -n "$a" ] && [ "$a" = "$b" ]; }
online_all() { [ "$(curl -s -m 5 $API/library | jq_ 'd["library"]["status"]')" = online ] && [ "$(snmp $LIB $E.1.4.0)" = 1 ]; }
check_online() { online_all && ok "$1：REST と SNMP は online のまま" || ng "$1：REST か SNMP が online でない"; }

wait_for 10 "libmgr の在庫（REST）がサーバの mtx status と一致" inv_same

step "1. MOVE MEDIUM の障害（HARDWARE ERROR 44h/00h）"
BEFORE=$(inventory)
R=$(sim PUT faults/move-medium '{"key":4,"asc":"44","ascq":0}')
[ "$(jq_ 'd["faults"]["moveMedium"]["sense"]' <<<"$R")" = 04/44/00 ] && ok "障害を入れた（state の sense 04/44/00）" || ng "障害が入らない：$R"
expect 04/44/00 "MOVE MEDIUM（スロット 1 → ドライブ 1）" $CH $(move_cdb $SLOT1 $DRV0)
expect 04/44/00 "MOVE MEDIUM（経路 2）" $CH2 $(move_cdb $SLOT1 $DRV0)
expect 04/44/00 "INITIALIZE ELEMENT STATUS（07h）" $CH $IES
expect 04/44/00 "INITIALIZE ELEMENT STATUS WITH RANGE（E7h）" $CH $IESR
expect GOOD "TEST UNIT READY" $CH $TUR
[ "$(inventory)" = "$BEFORE" ] && ok "READ ELEMENT STATUS（mtx status）は障害の前と同じ在庫" || ng "障害中の在庫が変わった"
srv "sudo mtx -f $CH load 1 0" >/dev/null 2>&1 && ng "障害中に mtx load が通った" || ok "障害中は mtx load が失敗する"
sleep 3
check_online "障害中"
sim PUT faults/move-medium '{"key":4,"asc":68,"ascq":0,"direction":"unload"}' >/dev/null
[ "$(simst 'd["faults"]["moveMedium"]["direction"]')" = unload ] && ok "direction=unload にした" || ng "direction が変わらない"
srv "sudo mtx -f $CH load 1 0" >/dev/null 2>&1 && ok "unload だけの障害なら入れるのは通る（スロット 1 → ドライブ 1）" || ng "unload だけの障害で入れられない"
expect 04/44/00 "出す MOVE MEDIUM（ドライブ 1 → スロット 1）" $CH $(move_cdb $DRV0 $SLOT1)
R=$(sim DELETE faults/move-medium)
[ "$(jq_ 'd["faults"]["moveMedium"]' <<<"$R")" = None ] && ok "障害を消した" || ng "障害が消えない：$R"
srv "sudo mtx -f $CH unload 1 0" >/dev/null 2>&1 && ok "消すと出せる" || ng "消しても出せない"
expect GOOD "INITIALIZE ELEMENT STATUS（消したあと）" $CH $IES
[ "$(inventory)" = "$BEFORE" ] && ok "在庫が元に戻った" || ng "在庫が元に戻らない"

step "2. 扉を開ける"
R=$(sim POST door/open)
[ "$(jq_ 'd["door"]["open"]' <<<"$R")" = True ] && ok "扉を開けた" || ng "扉が開かない：$R"
expect 02/04/03 "MOVE MEDIUM は NOT READY 04h/03h" $CH $(move_cdb $SLOT1 $DRV0)
expect 02/04/03 "INITIALIZE ELEMENT STATUS も 04h/03h" $CH $IES
expect GOOD "TEST UNIT READY はふつうに答える" $CH $TUR
[ "$(inventory)" = "$BEFORE" ] && ok "READ ELEMENT STATUS もふつうに答える" || ng "扉が開いていると在庫が読めない"
sim POST door/open '{"notReady":true}' >/dev/null
[ "$(simst 'd["door"]["notReady"]')" = True ] && ok "notReady にした" || ng "notReady にならない"
expect 02/04/03 "notReady：TEST UNIT READY も 04h/03h" $CH $TUR
srv "sudo mtx -f $CH status" >/dev/null 2>&1 && ng "notReady なのに mtx status が通った" || ok "notReady：READ ELEMENT STATUS（mtx status）も失敗する"
sleep 5
check_online "扉が開いている間（notReady、見回りはベンダーのビットで通る）"
LOADS=$(curl -s -m 5 $API/library | jq_ 'sum(1 for s in d["slots"] if s["full"])')
[ "$LOADS" -gt 0 ] 2>/dev/null && ok "扉が開いている間も libmgr は在庫を読めている（テープ $LOADS 本）" || ng "扉が開いている間に libmgr が在庫を読めない（$LOADS）"

step "3. 扉を閉じる（既定）：UNIT ATTENTION 28h/00h が全体で1回、障害も消える"
sim PUT faults/move-medium '{"key":4,"asc":"44","ascq":0}' >/dev/null
R=$(sim POST door/close)
[ "$(jq_ 'd["door"]["open"]' <<<"$R")" = False ] && [ "$(jq_ 'd["faults"]["moveMedium"]' <<<"$R")" = None ] && [ "$(jq_ 'd["unitAttentionPending"]' <<<"$R")" = True ] \
  && ok "扉が閉まり、障害が消え、UA を待っている" || ng "閉じたあとの state がおかしい：$R"
sleep 6
[ "$(simst 'd["unitAttentionPending"]')" = True ] && ok "6 秒たっても UA は残っている（見回りが取らない）" || ng "UA を見回りに取られた"
expect 06/28/00 "最初の命令（TEST UNIT READY）に UNIT ATTENTION 28h/00h" $CH $TUR
expect GOOD "2 回目は GOOD" $CH $TUR
expect GOOD "別の経路も GOOD（全体で1回）" $CH2 $TUR
[ "$(simst 'd["unitAttentionPending"]')" = False ] && ok "state の UA が消えた" || ng "state の UA が残っている"
srv "sudo mtx -f $CH load 1 0" >/dev/null 2>&1 && srv "sudo mtx -f $CH unload 1 0" >/dev/null 2>&1 \
  && ok "障害が消えていて、テープを出し入れできる" || ng "閉じたあと出し入れできない"

step "4. 扉を閉じる（clearFaults=false・becomingReadySeconds=3）"
sim PUT faults/move-medium '{"key":4,"asc":"44","ascq":0}' >/dev/null
sim POST door/open >/dev/null
R=$(sim POST door/close '{"clearFaults":false,"becomingReadySeconds":3}')
[ "$(jq_ 'd["faults"]["moveMedium"]["sense"]' <<<"$R")" = 04/44/00 ] && ok "障害は残る" || ng "障害が消えた：$R"
expect 02/04/01 "すぐの命令は NOT READY 04h/01h（becoming ready）" $CH $TUR
sleep 4
expect 06/28/00 "3 秒たつと UNIT ATTENTION 28h/00h" $CH $TUR
expect 04/44/00 "MOVE MEDIUM は障害のまま" $CH $(move_cdb $SLOT1 $DRV0)
sim DELETE faults/move-medium >/dev/null

step "5. 管理 API と監視に障害が出ていない"
check_online "最後"
wait_for 10 "libmgr の在庫（REST）がサーバの mtx status と一致" inv_same
EV=$(curl -s -m 5 "$API/events?n=200" | jq_ "len([e for e in d if e['kind']=='libraryStatusChange' and e['time']>='$MARK'])")
[ "$EV" = 0 ] && ok "試験の間に libmgr の状態の変化の出来事が無い" || ng "状態の変化の出来事が $EV 件"
if curl -s -m 3 -o /dev/null $ZBX; then
  sleep 30
  Z=$(zbx_lib1)
  [ "$Z" = "$ZBASE" ] && ok "Zabbix に lib1 の新しい障害が無い" || ng "Zabbix に lib1 の障害：$Z"
else
  echo "      （Zabbix が動いていないので見ない）"
fi

restore
trap - EXIT
[ "$(simst 'd["faults"]["moveMedium"] is None and not d["door"]["open"] and not d["unitAttentionPending"]')" = True ] \
  && ok "元に戻した（障害なし・扉は閉・UA なし）" || ng "元に戻らない：$(sim GET state)"
echo
[ $FAILS = 0 ] && echo "全部 OK" || { echo "NG が $FAILS 件"; exit 1; }
