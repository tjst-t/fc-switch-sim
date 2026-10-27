#!/bin/bash
# 障害訓練：冗長構成（lab/clab/fc-tape-ha.clab.yml）に障害を起こし、戻す。
# シナリオの一覧と、それぞれで何が起きるかは docs/failure-drills.md。
#
#   tools/drill.sh list                 シナリオの一覧
#   tools/drill.sh status               いまの様子（サーバ srv1 から見える装置の数・Zabbix の障害）
#   tools/drill.sh inject <シナリオ>     障害を起こす（手で訓練するとき。戻すのは recover）
#   tools/drill.sh recover <シナリオ>    戻す
#   tools/drill.sh run <シナリオ>...     起こす → 期待どおりか確かめる → 戻す → 戻ったか確かめる（OK・NG を出す）
#   tools/drill.sh run all              全部を順に
#
# 期待どおりかは、サーバ srv1 から見える SCSI 装置の数（ふだんは ライブラリ 2 台 × 5 個 × 経路 2 本 = 20 個）と、
# Zabbix の障害（Zabbix が動いていれば。lab/clab/zabbix.sh up と tests/zabbix-setup.py）で見る。
# run は経路が切れてから装置を消すまでの時間（dev_loss_tmo）を 10 秒にする（Linux の既定は 60 秒。手で訓練するときは既定のまま）。
set -uo pipefail
cd "$(dirname "$0")/.."

SRV=172.30.31.21; LIB1=172.30.31.22; LIB2=172.30.31.23
SWA=http://172.30.31.10:8080/api/v1; SWB=http://172.30.31.11:8080/api/v1
STATE=lab/run/drill; mkdir -p $STATE
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5)
vm() { local ip=$1; shift; ssh "${SSHO[@]}" lab@$ip "$@"; }
dx() { local n=$1; shift; sudo docker exec clab-fctape-ha-$n "$@"; }
redfish() { curl -sk -m 10 -u admin:password -H 'Content-Type: application/json' -X POST "https://$1/redfish/v1/Systems/1/Actions/ComputerSystem.Reset" -d "{\"ResetType\":\"$2\"}" -o /dev/null; }
devcount() { vm $SRV 'lsscsi | grep -c -E "tape|mediumx"; true' 2>/dev/null; }
set_dev_loss() { vm $SRV 'for r in /sys/class/fc_remote_ports/rport-*; do echo 10 | sudo tee $r/dev_loss_tmo >/dev/null 2>&1; done' 2>/dev/null; }
zabbix_up() { curl -s -m 3 -o /dev/null http://172.30.31.42:8080/api_jsonrpc.php; }
problems() {
  python3 - <<'PY' 2>/dev/null
import importlib.util
spec = importlib.util.spec_from_file_location("zs", "tests/zabbix-setup.py"); zs = importlib.util.module_from_spec(spec); spec.loader.exec_module(zs)
z = zs.Zbx("http://172.30.31.42:8080/api_jsonrpc.php")
for p in z.call("problem.get", {"output": ["name"]}):
    print(p["name"])
PY
}
# お知らせ（10 分で消える）を除いた障害
real_problems() { problems | grep -v -E "unmatched SNMP trap|zoning changed"; }

# ---- シナリオ ----
# 1 行 1 つ：名前|説明|起こしたあとの srv1 の装置の数（- は見ない）|Zabbix の障害（; 区切り、名前の一部）|待つ秒数
SCENARIOS='
switch-a-down|FC スイッチ A の停止（電源断・固まった。docker pause）|10|FC switch fab-a: REST not responding;FC switch fab-a: SNMP not responding|150
switch-b-down|FC スイッチ B の停止|10|FC switch fab-b: REST not responding;FC switch fab-b: SNMP not responding|150
cable-srv1-a|サーバ srv1 とスイッチ A の間のケーブル断|10|FC switch fab-a port 0 (eth1) went down (SNMP trap);FC switch fab-a port 0 (eth1) is Offline|90
cable-lib1-a|ライブラリ 1 とスイッチ A の間のケーブル断|15|FC switch fab-a port 1 (eth2) went down (SNMP trap);FC switch fab-a port 1 (eth2) is Offline|90
port-lib2-b|スイッチ B のライブラリ 2 のポートを無効（ポートの故障・設定の誤り）|15|FC switch fab-b port 2 (eth3) went down (SNMP trap);FC switch fab-b port 2 (eth3) is Disabled|90
zone-srv1-lib1|ゾーンの誤り：両方のファブリックから srv1 とライブラリ 1 のゾーンを消す|10|FC switch fab-a: zoning changed (SNMP trap);FC switch fab-b: zoning changed (SNMP trap)|90
lib1-offline|ライブラリ 1 のオフライン（ロボットが命令を受けない）|20|Tape library lib1 went offline (SNMP trap);Tape library lib1 is not online (SNMP);Tape library lib1 is not online (REST)|60
lib2-tapealert|ライブラリ 2 のドライブ 1 に TapeAlert 20（クリーニングが要る）|20|Drive 1 (LIB2_A1) on lib2: TapeAlert 20;Drive 1 (LIB2_A1) on lib2: TapeAlert raised (SNMP trap)|60
lib1-poweroff|ライブラリ 1 の電源断（Redfish の ForceOff）|10|Tape library lib1: SNMP not responding|150
srv1-reboot|サーバ srv1 の再起動（Redfish の ForceRestart）|-||240
'
field() { awk -F'|' -v n="$1" -v f="$2" '$1==n {print $f}' <<<"$SCENARIOS"; }
names() { awk -F'|' 'NF>1 {print $1}' <<<"$SCENARIOS"; }

inject() {
  case "$1" in
    switch-a-down) sudo docker pause clab-fctape-ha-fab-a >/dev/null ;;
    switch-b-down) sudo docker pause clab-fctape-ha-fab-b >/dev/null ;;
    cable-srv1-a) dx srv1 ip link set eth1 down ;;
    cable-lib1-a) dx lib1 ip link set eth1 down ;;
    port-lib2-b) curl -s -m 10 -X POST $SWB/ports/2/disable >/dev/null ;;
    zone-srv1-lib1)
      for f in a b; do
        api=$SWA; [ $f = b ] && api=$SWB
        curl -s -m 5 $api/zoning > $STATE/zoning-$f.json
        python3 -c 'import json,sys; z=json.load(open(sys.argv[1])); z["zones"]=[x for x in z["zones"] if not x["name"].startswith("z_srv1_lib1")]; print(json.dumps(z))' $STATE/zoning-$f.json \
          | curl -s -m 10 -X PUT $api/zoning -d @- >/dev/null
      done ;;
    lib1-offline) curl -s -m 10 -X POST http://$LIB1:8080/api/v1/library/offline >/dev/null ;;
    lib2-tapealert) curl -s -m 10 -X PUT http://$LIB2:8080/api/v1/drives/1/tapealert -d '{"flags":[20]}' >/dev/null ;;
    lib1-poweroff) redfish $LIB1 ForceOff ;;
    srv1-reboot)
      # 起こす前の時刻を残す（ログインし直しの確かめは、この時刻より後の記録だけを見る）
      date -u +%Y-%m-%dT%H:%M:%S.%NZ > $STATE/srv1-reboot.since
      sleep 1
      redfish $SRV ForceRestart ;;
    *) echo "知らないシナリオ $1" >&2; return 2 ;;
  esac
}

recover() {
  case "$1" in
    switch-a-down) sudo docker unpause clab-fctape-ha-fab-a >/dev/null ;;
    switch-b-down) sudo docker unpause clab-fctape-ha-fab-b >/dev/null ;;
    cable-srv1-a) dx srv1 ip link set eth1 up ;;
    cable-lib1-a) dx lib1 ip link set eth1 up ;;
    port-lib2-b) curl -s -m 10 -X POST $SWB/ports/2/enable >/dev/null ;;
    zone-srv1-lib1)
      for f in a b; do
        api=$SWA; [ $f = b ] && api=$SWB
        [ -f $STATE/zoning-$f.json ] && curl -s -m 10 -X PUT $api/zoning -d @$STATE/zoning-$f.json >/dev/null
      done ;;
    lib1-offline) curl -s -m 10 -X POST http://$LIB1:8080/api/v1/library/online >/dev/null ;;
    lib2-tapealert) curl -s -m 10 -X PUT http://$LIB2:8080/api/v1/drives/1/tapealert -d '{"flags":[]}' >/dev/null ;;
    lib1-poweroff) redfish $LIB1 On ;;
    srv1-reboot) ;;   # 再起動はひとりでに戻る
    *) echo "知らないシナリオ $1" >&2; return 2 ;;
  esac
}

# シナリオごとの、追加の確かめ（起こしたあと）
extra_check() {
  case "$1" in
    lib1-offline)
      CH=$(vm $SRV "for s in \$(lsscsi -g | awk '/mediumx/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw LIB1_A && { echo \$s; break; }; done")
      vm $SRV "sudo mtx -f $CH status" >/dev/null 2>&1 && ng "オフラインなのに srv1 からロボットの命令が通った" || ok "srv1 からライブラリ 1 のロボットの命令が通らない（Not Ready）" ;;
    srv1-reboot)
      # VM の再起動ではリンクは落ちない（模擬の限り）。スイッチには、同じ WWPN の FLOGI のやり直し（ログイン）が残る。
      # 起こしたあと（$STATE/srv1-reboot.since より後）の srv1 のログインが、両方のスイッチにあるかを見る
      wait_for 240 "両方のスイッチで srv1 がログインし直した（起こしたあとのログインの記録）" relogin_since "$(cat $STATE/srv1-reboot.since)" ;;
    lib2-tapealert)
      D=$(vm $SRV "for s in \$(lsscsi -g | awk '/tape/ {print \$NF}'); do sudo sg_vpd -p sn \$s 2>/dev/null | grep -qw LIB2_A1 && { echo \$s; break; }; done")
      vm $SRV "sudo sg_logs -p 0x2e $D" | grep -q "Cleaning required: 1" && ok "srv1 が TapeAlert のログで読めた（Cleaning required）" || ng "srv1 の TapeAlert のログにフラグが無い" ;;
  esac
}

FAILS=0
ok() { echo "  OK  $*"; }
ng() { echo "  NG  $*"; FAILS=$((FAILS+1)); }
wait_for() {
  local t=$1 what=$2; shift 2
  local end=$((SECONDS+t))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$what（$((t-(end-SECONDS))) 秒）"; return 0; fi
    sleep 3
  done
  ng "$what（$t 秒待っても成り立たない）"; return 1
}
devs_are() { [ "$(devcount)" = "$1" ]; }
has_problem() { problems | grep -q -F "$1"; }
no_real_problem() { [ -z "$(real_problems)" ]; }
setup_done() { vm $1 'test -f /var/log/fcsim-setup.rc'; }
# relogin_since <時刻>：両方のスイッチに、その時刻より後の srv1（WWPN 20:00:52:54:00:fc:0[12]:01）のログインの記録があるか
relogin_since() {
  local api
  for api in $SWA $SWB; do
    curl -s -m 5 "$api/events?n=200" | python3 -c '
import json, sys
since = sys.argv[1]
def ts(t):  # 2026-10-27T11:17:20.736446064Z → 比べられる形（秒の小数を 9 桁にそろえる）
    t = t.rstrip("Z"); a, _, f = t.partition("."); return a + "." + (f + "000000000")[:9]
ok = any(e.get("kind") == "login" and str(e.get("wwpn", "")).endswith(":01") and e.get("wwpn", "").startswith("20:00:52:54:00:fc:0")
         and ts(e["time"]) > ts(since) for e in json.load(sys.stdin))
sys.exit(0 if ok else 1)' "$1" || return 1
  done
}

run_one() {
  local n=$1 desc want probs wait
  desc=$(field "$n" 2); want=$(field "$n" 3); probs=$(field "$n" 4); wait=$(field "$n" 5)
  [ -n "$desc" ] || { echo "知らないシナリオ $n" >&2; FAILS=$((FAILS+1)); return; }
  echo "== $n：$desc"
  local z=0; zabbix_up && z=1
  set_dev_loss
  devs_are 20 || { ng "始める前の装置が 20 個でない（$(devcount) 個）。前のシナリオが戻っていないかも"; return; }
  inject "$n" && ok "起こした"
  [ "$want" = - ] || wait_for "$wait" "srv1 の装置が $want 個になる" devs_are "$want"
  if [ $z = 1 ] && [ -n "$probs" ]; then
    IFS=';' read -ra ps <<<"$probs"
    for p in "${ps[@]}"; do wait_for "$wait" "Zabbix の障害：$p" has_problem "$p"; done
  fi
  extra_check "$n"
  recover "$n" && ok "戻した"
  case "$n" in
    lib1-poweroff|srv1-reboot)
      ip=$LIB1; [ $n = srv1-reboot ] && ip=$SRV
      sleep 20
      wait_for 400 "起動して FC の設定が終わる" setup_done $ip ;;
  esac
  wait_for 300 "srv1 の装置が 20 個に戻る" devs_are 20
  set_dev_loss
  [ $z = 1 ] && wait_for 240 "Zabbix の障害が消える（お知らせを除く）" no_real_problem
}

case "${1:-list}" in
  list)
    awk -F'|' 'NF>1 {printf "  %-16s %s\n", $1, $2}' <<<"$SCENARIOS" ;;
  status)
    echo "srv1 から見える装置：$(devcount) 個（ふだんは 20）"
    if zabbix_up; then echo "Zabbix の障害："; problems | sed 's/^/  /'; else echo "Zabbix：動いていない"; fi ;;
  inject|recover)
    [ $# -ge 2 ] || { echo "シナリオの名前が要る" >&2; exit 2; }
    "$1" "$2" && echo "$1：$2" ;;
  run)
    shift
    [ "${1:-}" = all ] && set -- $(names)
    for n in "$@"; do run_one "$n"; done
    echo
    if [ $FAILS = 0 ]; then echo "全部 OK"; else echo "NG が $FAILS 件"; fi
    exit $((FAILS > 0)) ;;
  *) sed -n '2,15p' "$0"; exit 2 ;;
esac
