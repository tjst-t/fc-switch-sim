#!/bin/bash
# 冗長構成（lab/clab/fc-tape-ha.clab.yml：サーバ 2・ライブラリ 2・ファブリック A と B）を通しで確かめる。
#
#   tests/e2e-ha.sh            いま動いている構成を確かめる
#   tests/e2e-ha.sh --deploy   作り直して（prepare ha → deploy）から確かめる
#   tests/e2e-ha.sh --deploy --destroy   終わったら壊す
#
# 確かめること：
#   1. 8 つの N_Port（4 台 × 2 ファブリック）がログインし、サーバの fc_host が A・B とも Online
#   2. サーバから、ライブラリ 2 台 × 装置 5 個が、経路 A と B の両方で見える（20 個。シリアル番号ごとに 2 本）
#   3. 両方のライブラリで：経路 A のロボットでテープを入れ、経路 A のドライブで書き、経路 B の同じドライブで読み戻して一致
#   4. 経路 B で書いている最中にスイッチ A を止めても、書き込みは成功し中身も一致する
#      スイッチ A が止まっている間、経路 A の装置が消え、経路 B の 10 個は残る
#   5. スイッチ A を戻すと、経路 A の装置が戻る（20 個）
#   6. スイッチ B でライブラリ 2 のポートだけを無効にすると、ライブラリ 2 の経路 B だけが消え（15 個）、戻すと戻る
#   7. サーバ 2 台：srv2 にはゾーンどおりライブラリ 2 だけ（10 個）が見える。2 台から別々のライブラリへ同時に書いて一致。
#      スイッチ A のゾーンに srv2 とライブラリ 1 を足すと、srv2 にライブラリ 1 の経路 A（5 個）が増え、外すと消える
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
TOPO=lab/clab/run-ha.clab.yml
API_A=http://172.30.31.10:8080/api/v1
API_B=http://172.30.31.11:8080/api/v1
SRV=172.30.31.21; SRV2=172.30.31.24
SSHO=(-i .cache/id_lab -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
vm() { local ip=$1; shift; ssh "${SSHO[@]}" lab@$ip "$@"; }
srv() { vm $SRV "$@"; }
srv2() { vm $SRV2 "$@"; }
ON=$SRV   # paths・devcount が見るサーバ
FAILS=0
ok() { echo "  OK  $*"; }
ng() { echo "  NG  $*"; FAILS=$((FAILS+1)); }
step() { echo "== $*"; }
wait_for() {
  local t=$1 what=$2; shift 2
  local end=$((SECONDS+t))
  while [ $SECONDS -lt $end ]; do
    if "$@" >/dev/null 2>&1; then ok "$what（$((t-(end-SECONDS))) 秒）"; return 0; fi
    sleep 2
  done
  ng "$what（$t 秒待っても成り立たない）"; diag; return 1
}
diag() {
  echo "      --- サーバの経路"
  srv 'for h in /sys/class/fc_host/host*; do echo "$(basename $h) $(cat $h/port_name $h/fabric_name $h/port_state | tr "\n" " ")"; done; for r in /sys/class/fc_remote_ports/rport-*; do echo "$(basename $r) $(cat $r/port_name $r/port_id $r/port_state | tr "\n" " ")"; done; echo "装置 $(lsscsi | grep -c -E "tape|mediumx"; true) 個"' 2>&1 | sed 's/^/      /'
  for api in $API_A $API_B; do
    echo "      --- $api"
    curl -s -m 5 "$api/events?n=6" | grep message | sed 's/^ */      /'
  done
}
# サーバから見える装置の表：「シリアル番号 ファブリック(A|B) /dev/sgN /dev/nstN（ドライブだけ）」
paths() {
  vm $ON 'FA=$(for h in /sys/class/fc_host/host*; do [ "$(cat $h/fabric_name)" = 0x100000051efc000a ] && basename $h; done)
    lsscsi -g | grep -E "tape|mediumx" | while read hctl type rest; do
      sg=$(echo "$rest" | awk "{print \$NF}"); st=$(echo "$rest" | awk "{print \$(NF-1)}")
      h=host$(echo "$hctl" | tr -d "[" | cut -d: -f1)
      fab=B; [ "$h" = "$FA" ] && fab=A
      ser=$(sudo sg_vpd -p sn $sg 2>/dev/null | awk -F: "/serial number:/ {gsub(/ /,\"\",\$2); print \$2}" | tail -1)
      nst=-; [ "$type" = tape ] && nst=/dev/n${st#/dev/}
      echo "$ser $fab $sg $nst"
    done | sort'
}
devcount() { vm $ON 'lsscsi | grep -c -E "tape|mediumx"; true' 2>/dev/null; }
devs_are() { [ "$(devcount)" = "$1" ]; }
set_dev_loss() { vm $ON 'for r in /sys/class/fc_remote_ports/rport-*; do echo 10 | sudo tee $r/dev_loss_tmo >/dev/null 2>&1; done'; }
# 経路表から 1 つ引く：pick <表> <シリアル番号> <A|B> <列 3=sg・4=nst>
pick() { awk -v s="$2" -v f="$3" -v c="$4" '$1==s && $2==f {print $c; exit}' <<<"$1"; }

if [ $DEPLOY = 1 ]; then
  step "作り直す"
  sudo docker unpause clab-fctape-ha-fab-a >/dev/null 2>&1 || true
  sudo containerlab destroy -t $TOPO --cleanup >/dev/null 2>&1 || true
  lab/clab/prepare.sh ha >/dev/null || { echo "prepare に失敗" >&2; exit 1; }
  sudo containerlab deploy -t $TOPO >/dev/null || { echo "deploy に失敗" >&2; exit 1; }
fi

step "0. 決まった状態に戻す（スイッチ A を動かす、ポートは全部有効）"
sudo docker unpause clab-fctape-ha-fab-a >/dev/null 2>&1 || true
api_up() { curl -s -m 3 -o /dev/null $API_A/switch && curl -s -m 3 -o /dev/null $API_B/switch; }
wait_for 120 "両方のスイッチの API が答える" api_up
for api in $API_A $API_B; do for i in 0 1 2 3; do curl -s -m 5 -X POST $api/ports/$i/enable >/dev/null; done; done

step "1. ログイン"
for ip in $SRV $SRV2 172.30.31.22 172.30.31.23; do
  wait_for 300 "$ip の設定が終わった" vm $ip 'test -f /var/log/fcsim-setup.rc'
  [ "$(vm $ip cat /var/log/fcsim-setup.rc)" = 0 ] && ok "$ip の設定が成功" || ng "$ip の設定が失敗（/var/log/fcsim-setup.log）"
done
ns4() { [ "$(curl -s -m 5 $1/nameserver | grep -c '"fcid"')" = 4 ]; }
wait_for 120 "スイッチ A のネームサーバに 4 件" ns4 $API_A
wait_for 120 "スイッチ B のネームサーバに 4 件" ns4 $API_B
st=$(srv 'cat /sys/class/fc_host/host*/port_state | tr "\n" " "')
[ "$st" = "Online Online " ] && ok "サーバの fc_host が 2 つとも Online" || ng "サーバの fc_host：$st"

step "2. 装置が両方の経路で見える"
wait_for 90 "装置 20 個（ライブラリ 2 台 × 5 個 × 経路 2 本）" devs_are 20
set_dev_loss
P=$(paths)
for lib in 1 2; do
  for sfx in "" 1 2 3 4; do
    s=LIB${lib}_A$sfx
    n=$(awk -v s=$s '$1==s' <<<"$P" | awk '{print $2}' | sort | tr -d '\n')
    [ "$n" = AB ] || ng "$s の経路が A と B でない（$n）"
  done
done
[ "$(awk '{print $1}' <<<"$P" | sort -u | wc -l)" = 10 ] && ok "シリアル番号 10 種類が、それぞれ経路 A と B で見える" || ng "シリアル番号の数が合わない"

step "3. 両方のライブラリで、経路 A で書き経路 B で読む"
for lib in 1 2; do
  CH=$(pick "$P" LIB${lib}_A A 3); CHB=$(pick "$P" LIB${lib}_A B 3)
  TA=$(pick "$P" LIB${lib}_A1 A 4); TB=$(pick "$P" LIB${lib}_A1 B 4)
  out=$(srv "set -e; cd /var/tmp
    SLOT=\$(sudo mtx -f $CH status | awk -F'[ :]+' '/Storage Element [0-9]+:Full/ && !/IMPORT/ {print \$4; exit}')
    TAG=\$(sudo mtx -f $CH status | awk -v s=\$SLOT -F'VolumeTag=' '\$0 ~ \"Storage Element \"s\":Full\" {gsub(/ /,\"\",\$2); print \$2; exit}')
    sudo mtx -f $CH load \$SLOT 0 >/dev/null
    sudo head -c 64000000 /dev/urandom | sudo tee w.bin >/dev/null
    sudo mt -f $TA rewind; sudo timeout 60 dd if=w.bin of=$TA bs=1M status=none; sudo mt -f $TA rewind
    sudo timeout 60 dd if=$TB of=r.bin bs=1M status=none; sudo mt -f $TB rewind
    sudo cmp -s w.bin r.bin && echo MATCH
    sudo mtx -f $CHB unload \$SLOT 0 >/dev/null
    sudo rm -f w.bin r.bin
    echo \"DONE \$TAG\"" 2>&1)
  if grep -q MATCH <<<"$out" && grep -q DONE <<<"$out"; then
    ok "ライブラリ $lib：テープ $(grep DONE <<<"$out" | cut -d' ' -f2) を経路 A で入れ、A で 64MB 書き、B で読み戻して一致、B で出した"
  else
    ng "ライブラリ $lib：読み書きが通らない"; echo "$out" | tail -4 | sed 's/^/      /'
  fi
done

step "4. 経路 B で書いている最中にスイッチ A を止める"
CHB=$(pick "$P" LIB2_A B 3); TB=$(pick "$P" LIB2_A2 B 4)
srv "set -e; cd /var/tmp; sudo mtx -f $CHB load 2 1 >/dev/null; sudo head -c 400000000 /dev/urandom | sudo tee w4.bin >/dev/null; sudo mt -f $TB rewind" \
  || ng "準備（テープを入れる）に失敗"
srv "cd /var/tmp; sudo timeout 120 dd if=w4.bin of=$TB bs=256k status=none && echo WRITE_OK > w4.rc || echo WRITE_NG > w4.rc" &
WPID=$!
sleep 1
sudo docker pause clab-fctape-ha-fab-a >/dev/null && ok "スイッチ A を止めた（docker pause）" || ng "スイッチ A を止められない"
wait $WPID
[ "$(srv cat /var/tmp/w4.rc)" = WRITE_OK ] && ok "経路 B の書き込み（400MB）は成功" || ng "経路 B の書き込みが失敗"
r=$(srv "cd /var/tmp; sudo mt -f $TB rewind; sudo timeout 60 dd if=$TB of=r4.bin bs=256k status=none; sudo cmp -s w4.bin r4.bin && echo MATCH; sudo mt -f $TB rewind; sudo rm -f w4.bin r4.bin w4.rc" 2>&1)
grep -q MATCH <<<"$r" && ok "読み戻して一致（スイッチ A が止まったまま）" || ng "読み戻した中身が一致しない"
wait_for 90 "経路 A の装置が消え、経路 B の 10 個だけになる" devs_are 10
P2=$(paths)
[ "$(awk '$2=="B"' <<<"$P2" | wc -l)" = 10 ] && [ "$(awk '$2=="A"' <<<"$P2" | wc -l)" = 0 ] && ok "残っているのは経路 B の 10 個" || ng "残っている経路が合わない"
srv "sudo mtx -f $CHB unload 2 1 >/dev/null" || ng "テープを出せない（経路 B のロボット）"

step "5. スイッチ A を戻す"
sudo docker unpause clab-fctape-ha-fab-a >/dev/null && ok "スイッチ A を戻した"
wait_for 120 "経路 A の装置が戻る（20 個）" devs_are 20
set_dev_loss

step "6. スイッチ B でライブラリ 2 のポートだけを無効にする"
curl -s -m 5 -X POST $API_B/ports/2/disable >/dev/null
wait_for 90 "ライブラリ 2 の経路 B だけが消える（15 個）" devs_are 15
P3=$(paths)
n=$(awk '$1 ~ /^LIB2_/ && $2=="B"' <<<"$P3" | wc -l); m=$(awk '$1 ~ /^LIB1_/' <<<"$P3" | wc -l)
[ "$n" = 0 ] && [ "$m" = 10 ] && ok "消えたのはライブラリ 2 の経路 B だけ" || ng "消えた経路が合わない（LIB2 の B：$n、LIB1：$m）"
curl -s -m 5 -X POST $API_B/ports/2/enable >/dev/null
wait_for 120 "有効に戻すと戻る（20 個）" devs_are 20

step "7. サーバ 2 台"
ON=$SRV2
wait_for 60 "srv2 に装置 10 個（ライブラリ 2 だけ × 経路 2 本）" devs_are 10
set_dev_loss
Q=$(paths)
[ "$(awk '$1 ~ /^LIB1_/' <<<"$Q" | wc -l)" = 0 ] && [ "$(awk '$1 ~ /^LIB2_/' <<<"$Q" | wc -l)" = 10 ] \
  && ok "srv2 にはライブラリ 1 が見えず、ライブラリ 2 が経路 A・B で見える（ゾーンどおり）" || ng "srv2 の見え方がゾーンと合わない"
ON=$SRV
P=$(paths)
# srv1 → ライブラリ 1 のドライブ 2（経路 A）、srv2 → ライブラリ 2 のドライブ 2（経路 B）へ同時に書く
# （ドライブ 1・2 は LTO-10、3・4 は LTO-6。テープは LTO-10 なので 3・4 には入らない＝本物と同じく断られる）
CH1=$(pick "$P" LIB1_A A 3); T1=$(pick "$P" LIB1_A2 A 4)
CH2=$(pick "$Q" LIB2_A B 3); T2=$(pick "$Q" LIB2_A2 B 4)
job() { # 命令 ロボット スロット ドライブの番号 ドライブ
  local cmd=$1 ch=$2 slot=$3 de=$4 t=$5
  $cmd "set -e; cd /var/tmp; sudo mtx -f $ch load $slot $de >/dev/null
    sudo head -c 200000000 /dev/urandom | sudo tee w7.bin >/dev/null
    sudo mt -f $t rewind; sudo timeout 90 dd if=w7.bin of=$t bs=1M status=none; sudo mt -f $t rewind
    sudo timeout 90 dd if=$t of=r7.bin bs=1M status=none; sudo mt -f $t rewind
    sudo cmp -s w7.bin r7.bin && echo MATCH
    sudo mtx -f $ch unload $slot $de >/dev/null; sudo rm -f w7.bin r7.bin" 2>&1
}
job srv $CH1 3 1 $T1 > /tmp/e2e-ha-j1.txt & J1=$!
job srv2 $CH2 3 1 $T2 > /tmp/e2e-ha-j2.txt & J2=$!
wait $J1 $J2
grep -q MATCH /tmp/e2e-ha-j1.txt && ok "srv1：ライブラリ 1 のドライブ 2 に 200MB 書いて一致（同時）" || { ng "srv1 の同時書き込み"; tail -3 /tmp/e2e-ha-j1.txt | sed 's/^/      /'; }
grep -q MATCH /tmp/e2e-ha-j2.txt && ok "srv2：ライブラリ 2 のドライブ 2 に 200MB 書いて一致（同時）" || { ng "srv2 の同時書き込み"; tail -3 /tmp/e2e-ha-j2.txt | sed 's/^/      /'; }
# ゾーンの変更：スイッチ A で srv2 にライブラリ 1 を見せる
ZA=$(curl -s -m 5 $API_A/zoning)
ZA2=$(python3 -c 'import json,sys; z=json.loads(sys.argv[1]); z["zones"].append({"name":"z_srv2_lib1_a","members":["srv2_a","lib1_a"]}); print(json.dumps(z))' "$ZA")
curl -s -m 5 -X PUT $API_A/zoning -d "$ZA2" >/dev/null
ON=$SRV2
wait_for 60 "ゾーンを足すと、srv2 にライブラリ 1 の経路 A が増える（15 個）" devs_are 15
[ "$(paths | awk '$1 ~ /^LIB1_/ {print $2}' | sort -u | tr -d '\n')" = A ] && ok "増えたのはライブラリ 1 の経路 A だけ" || ng "増えた経路が合わない"
set_dev_loss
curl -s -m 5 -X PUT $API_A/zoning -d "$ZA" >/dev/null
wait_for 60 "ゾーンを戻すと消える（10 個）" devs_are 10
ON=$SRV
wait_for 30 "srv1 は変わらず 20 個" devs_are 20

step "スイッチの数え"
for api in $API_A $API_B; do
  echo "  $api：$(curl -s -m 5 $api/ports | grep -E '"(kernelDrops|crcErrors|txErrors)"' | tr -d ' ,' | paste -sd' ')"
done

if [ $DESTROY = 1 ]; then
  step "壊す"
  sudo containerlab destroy -t $TOPO --cleanup >/dev/null 2>&1
fi
echo
if [ $FAILS = 0 ]; then echo "全部 OK"; else echo "NG が $FAILS 件"; fi
exit $((FAILS > 0))
