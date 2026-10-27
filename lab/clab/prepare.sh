#!/bin/bash
# ContainerLab の構成の下ごしらえ：スイッチのイメージ、VM のディスク（共通イメージの差分）と cloud-init の種。
# 種には VM の中で流すスクリプトを入れ、起動したら自動で FC の設定まで済ませる。
#   prepare.sh [single|ha]
#     single：fc-tape.clab.yml    → run.clab.yml（サーバ 1・ライブラリ 1・スイッチ 1）
#     ha    ：fc-tape-ha.clab.yml → run-ha.clab.yml（サーバ 2・ライブラリ 2・ファブリック A と B）
#     FCSIM_LIBMGR=1：single でもライブラリに管理 API（libmgr）を入れる。FCSIM_KEEP_DISK=1：差分ディスクと種を作り直さずに使う
#   prepare.sh seed -d <置き場> -n <ノード名> -r server|library -f <FC の NIC の MAC[,MAC]> [ほかの引数]
#     外部のトポロジ向け：VM 1 台ぶんの種（seed.iso）とディスク（disk.qcow2）だけを <置き場> に作る。
#     スイッチのイメージも .clab.yml も作らない。引数とコンテナへの bind は docs/external-topology.md
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
CACHE=$ROOT/.cache
# FCSIM_BASE=<パス> で共通イメージを替えられる（VM のディスクはこの差分になる）
BASE=$(realpath -m "${FCSIM_BASE:-$CACHE/fcsim-base.qcow2}")
[ -f "$BASE" ] || { echo "$BASE が無い。先に lab/images/build-base.sh" >&2; exit 1; }
TOPO=${1:-single}
PUBFILE=$CACHE/id_lab.pub
b64() { base64 -w0 "$1"; }
# テープライブラリの管理 API（libmgr）：使う種を作るときだけ build する。ホストに Go が無ければ golang のコンテナで
GOIMG="golang:1.26-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af"
LIBMGR_GZ=""
libmgr_gz() {
  [ -n "$LIBMGR_GZ" ] && return
  local GO; GO=$(PATH=$PATH:/usr/local/go/bin command -v go || true)
  [ "${FCSIM_LIBMGR_BUILD:-}" != docker ] || GO=""   # FCSIM_LIBMGR_BUILD=docker でホストに Go があってもコンテナで
  mkdir -p "$ROOT/bin"
  if [ -n "$GO" ]; then
    (cd "$ROOT" && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o bin/libmgr ./cmd/libmgr)
  else
    sudo docker run --rm -v "$ROOT:/src" -w /src -e CGO_ENABLED=0 -u "$(id -u):$(id -g)" -e GOCACHE=/tmp/gocache -e GOPATH=/tmp/gopath \
      "$GOIMG" go build -trimpath -ldflags "-s -w" -o bin/libmgr ./cmd/libmgr
  fi
  LIBMGR_GZ=$(gzip -9c "$ROOT/bin/libmgr" | base64 -w0)
}
# FCSIM_TAPE_DELAYS=1 なら、ライブラリ VM の mhVTL にロード・巻き戻し・位置決めの時間を設定する（既定は無効。有効にすると
# 読み書き・mt rewind・mt fsf 等が数秒〜数十秒ブロックされ、既存の e2e テストのタイムアウトと噛み合わない）。
# FCSIM_DEBUG=1 なら、VM の中で libfc・libfcoe のデバッグのログを起動時から出す（調査用）
if [ "${FCSIM_DEBUG:-}" = 1 ]; then
  DEBUGCMD='modprobe libfcoe; echo 0x3f > /sys/module/libfc/parameters/debug_logging; echo 0xff > /sys/module/libfcoe/parameters/debug_logging'
else
  DEBUGCMD=true
fi
# FCSIM_KEEP_DISK=1（seed では -K）なら、置き場に前に作った差分ディスクと種があれば作り直さずにそのまま使う
# （VM の中の状態＝テープの中身などを残したまま起こし直す）。差分ディスクの元が今の共通イメージと違えば止まる。
KEEP_DISK=${FCSIM_KEEP_DISK:-}
BASE_SHA=""
base_sha() { [ -n "$BASE_SHA" ] || BASE_SHA=$(sha256sum "$BASE" | cut -d' ' -f1); echo "$BASE_SHA"; }
# 置き場の差分ディスクを使い続けてよいか（元のパスと中身の sha256 が、作ったときの記録 disk.base と今の共通イメージで同じか）
keep_ok() {
  local d=$1 rec back
  [ -f "$d/disk.qcow2" ] && [ -f "$d/seed.iso" ] || return 1   # 無ければ作る
  rec="$d/disk.base"
  [ -f "$rec" ] || { echo "$d/disk.qcow2 に元の記録（disk.base）が無い。使い続けられるか分からないので止まる（作り直すなら FCSIM_KEEP_DISK を外す）" >&2; exit 3; }
  back=$(qemu-img info --force-share --output=json "$d/disk.qcow2" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("full-backing-filename",""))')
  if [ "$back" != "$BASE" ] || [ "$(sed -n 's/^backing=//p' "$rec")" != "$BASE" ]; then
    echo "$d/disk.qcow2 の元（$back）が今の共通イメージ（$BASE）と違う。止まる" >&2; exit 3
  fi
  if [ "$(sed -n 's/^base_sha256=//p' "$rec")" != "$(base_sha)" ]; then
    echo "$d/disk.qcow2 を作ったあとに共通イメージ $BASE の中身が変わった（sha256 が違う）。古い差分は使えないので止まる" >&2; exit 3
  fi
  return 0
}
# 種の作り方を変える値（seed の前に決める。既定は lab/clab の構成のもの）
MGMT_MAC=52:54:00:aa:00:01   # 管理の NIC の MAC（QEMU_EXTRA_ARGS の -nic user,mac= と同じにする）
MGMT_ADDR=""                 # 空なら DHCP（qemu の user-net）。"10.0.0.5/24" のように書くと固定
MGMT_GW=""
MGMT_DNS=""
seed() { # 名前 役割の命令 [libmgr の引数（ライブラリだけ）]。置き場は SEED_DIR（無ければ $RUN/名前）
  local name=$1 cmd=$2 d=${SEED_DIR:-$RUN/$1} lm=${3:-}
  local lmfiles="" lmcmd="true" PUB
  if [ -n "$KEEP_DISK" ] && keep_ok "$d"; then
    # 種も前のものを使う（instance-id が変わらないので、cloud-init の初回の設定はもう走らない。引数を変えても効かない）
    echo "$name：前の差分ディスクと種をそのまま使う（$d）"
    return 0
  fi
  PUB=$(cat "$PUBFILE")
  if [ -n "$lm" ]; then
    libmgr_gz
    lmfiles="  - path: /usr/local/bin/libmgr
    permissions: \"0755\"
    encoding: gz+b64
    content: $LIBMGR_GZ
  - path: /etc/systemd/system/libmgr.service
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/libmgr/libmgr.service")
  - path: /etc/snmp/snmpd.conf
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/libmgr/snmpd.conf")
  - path: /etc/default/libmgr
    content: |
      LIBMGR_ARGS=\"$lm\""
    lmcmd="systemctl daemon-reload; systemctl enable --now libmgr; systemctl restart snmpd"
  fi
  mkdir -p "$d"
  rm -f "$d/disk.qcow2"
  qemu-img create -q -f qcow2 -F qcow2 -b "$BASE" "$d/disk.qcow2" 20G
  printf 'backing=%s\nbase_sha256=%s\ncreated=%s\n' "$BASE" "$(base_sha)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$d/disk.base"
  cat > "$d/user-data" <<UD
#cloud-config
hostname: $name
ssh_authorized_keys:
  - $PUB
write_files:
  - path: /opt/fcsim/fcoe-up.sh
    permissions: "0755"
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/fcoe-up.sh")
  - path: /opt/fcsim/lib-setup.sh
    permissions: "0755"
    encoding: b64
    content: $(b64 "$ROOT/lab/images/guest/lib-setup.sh")
$lmfiles
  # FC の設定は起動のたびに流す（MTU・スキャッタギャザー・LIO のターゲット・FCoE は再起動で消えるか、順番が要るため）
  - path: /etc/systemd/system/fcsim-setup.service
    content: |
      [Unit]
      Description=fc-switch-sim FC setup (FCoE, LIO target, mhVTL)
      Wants=network-online.target
      After=network-online.target systemd-modules-load.service
      [Service]
      Type=oneshot
      RemainAfterExit=yes
      TimeoutStartSec=600
      Environment=FCSIM_TAPE_DELAYS=${FCSIM_TAPE_DELAYS:-}
      ExecStartPre=/bin/rm -f /var/log/fcsim-setup.rc
      ExecStart=/bin/sh -c '$cmd > /var/log/fcsim-setup.log 2>&1; echo \$\$? > /var/log/fcsim-setup.rc'
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [sh, -c, '$DEBUGCMD']
  - [sh, -c, 'systemctl daemon-reload; systemctl enable fcsim-setup; systemctl start fcsim-setup']
  - [sh, -c, '$lmcmd']
UD
  printf 'instance-id: %s-%s\nlocal-hostname: %s\n' "$name" "$(date +%s)" "$name" > "$d/meta-data"
  {
    printf 'version: 2\nethernets:\n  mgmt0:\n    match:\n      macaddress: "%s"\n    set-name: mgmt0\n' "$MGMT_MAC"
    if [ -z "$MGMT_ADDR" ]; then
      printf '    dhcp4: true\n'
    else
      printf '    dhcp4: false\n    addresses: ["%s"]\n' "$MGMT_ADDR"
      [ -z "$MGMT_GW" ] || printf '    routes:\n      - to: default\n        via: %s\n' "$MGMT_GW"
      [ -z "$MGMT_DNS" ] || printf '    nameservers:\n      addresses: [%s]\n' "$MGMT_DNS"
    fi
  } > "$d/network-config"
  cloud-localds -N "$d/network-config" "$d/seed.iso" "$d/user-data" "$d/meta-data"
}
usage_seed() {
  cat >&2 <<'U'
prepare.sh seed -d <置き場> -n <ノード名> -r server|library -f <FC の NIC の MAC[,MAC]> [引数]
  -I "<WWPN> ..."   ライブラリ：LIO のターゲットに許すイニシエータ（サーバの WWPN、0x2000<MAC の 12 桁> の形）。要る
  -i <番号>         ライブラリ：番号（1〜。シリアル番号 LIB<番号>_A・NAA・バーコードを分ける。既定 1）
  -L "<引数>"       ライブラリ：管理 API（libmgr）を入れて、この引数で動かす（例 "-traps 10.0.0.30:162"。"" なら引数なし）
  -m <MAC>          管理の NIC の MAC（既定 52:54:00:aa:00:01）
  -a <アドレス/長さ> 管理の NIC を固定のアドレスに（既定は DHCP）。-g <ゲートウェイ>・-s <DNS> も
  -k <ファイル>     VM のユーザ lab に入れる ssh の公開鍵（既定 .cache/id_lab.pub）
  -K                前に作った差分ディスクと種があれば、作り直さずに使う（共通イメージが変わっていれば止まる）
U
  exit 2
}
case "$TOPO" in
  seed)
    shift
    DIR="" NAME="" ROLE="" FCMACS="" INIS="" LIBIDX=1 LMARGS="" WANT_LM=0
    while getopts "d:n:r:f:I:i:L:m:a:g:s:k:K" o; do
      case $o in
        d) DIR=$OPTARG ;; n) NAME=$OPTARG ;; r) ROLE=$OPTARG ;; f) FCMACS=$OPTARG ;;
        I) INIS=$OPTARG ;; i) LIBIDX=$OPTARG ;; L) LMARGS=$OPTARG; WANT_LM=1 ;;
        m) MGMT_MAC=$OPTARG ;; a) MGMT_ADDR=$OPTARG ;; g) MGMT_GW=$OPTARG ;; s) MGMT_DNS=$OPTARG ;;
        k) PUBFILE=$OPTARG ;; K) KEEP_DISK=1 ;; *) usage_seed ;;
      esac
    done
    [ -n "$DIR" ] && [ -n "$NAME" ] && [ -n "$FCMACS" ] || usage_seed
    [[ "$NAME" =~ ^[a-zA-Z0-9][a-zA-Z0-9-]*$ ]] || { echo "ノード名 $NAME はホスト名に使える文字だけにする" >&2; exit 2; }
    [[ "$FCMACS" =~ ^([0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5})(,[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5})*$ ]] || { echo "FC の MAC が読めない：$FCMACS" >&2; exit 2; }
    [ -f "$PUBFILE" ] || { echo "公開鍵 $PUBFILE が無い" >&2; exit 2; }
    SEED_DIR=$(realpath -m "$DIR")
    case "$ROLE" in
      server) [ $WANT_LM = 0 ] || { echo "-L はライブラリだけ" >&2; exit 2; }
              seed "$NAME" "/opt/fcsim/fcoe-up.sh $FCMACS" ;;
      library) [ -n "$INIS" ] || { echo "ライブラリには -I（許すイニシエータ）が要る" >&2; exit 2; }
               if [ $WANT_LM = 1 ]; then LMARGS=${LMARGS:- }; fi
               seed "$NAME" "/opt/fcsim/lib-setup.sh -i $LIBIDX $FCMACS $INIS" "$LMARGS" ;;
      *) usage_seed ;;
    esac
    echo "用意できた：$SEED_DIR（seed.iso・disk.qcow2。disk.qcow2 は $BASE の差分）"
    exit 0 ;;
  single) SRC=fc-tape.clab.yml; OUT=run.clab.yml; RUN=run ;;
  ha) SRC=fc-tape-ha.clab.yml; OUT=run-ha.clab.yml; RUN=run-ha ;;
  *) echo "知らない構成 $TOPO（single・ha・seed）" >&2; exit 2 ;;
esac
if [ "${REBUILD:-1}" = 1 ]; then
  sudo docker build -q -f "$ROOT/image/Dockerfile" -t fc-switch-sim/fcsw:dev "$ROOT" >/dev/null
  sudo docker build -q -f "$ROOT/image/mon/Dockerfile" -t fc-switch-sim/mon:dev "$ROOT" >/dev/null
fi
sed "s|__CACHE__|$CACHE|g" $SRC > $OUT
if [ "$TOPO" = single ]; then
  seed srv1 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:00:01"
  if [ "${FCSIM_LIBMGR:-}" = 1 ]; then
    # FCSIM_LIBMGR=1：single でもライブラリに管理 API（libmgr：REST :8080・SNMP :161・/_sim/v1）を入れる（既定は入れない）。
    # 引数は FCSIM_LIBMGR_ARGS（例 "-traps 172.30.30.30:162"）。lib1 の管理の NIC に 8080・161 の転送を足す
    seed lib1 "/opt/fcsim/lib-setup.sh 52:54:00:fc:00:02 0x2000525400fc0001" "${FCSIM_LIBMGR_ARGS:- }"
    awk '/^    lib1:/ {inlib=1} inlib && /QEMU_EXTRA_ARGS:/ && !done {sub(/hostfwd=tcp::22-:22"/, "hostfwd=tcp::22-:22,hostfwd=tcp::8080-:8080,hostfwd=udp::161-:161\""); done=1} {print}' $OUT > $OUT.tmp && mv $OUT.tmp $OUT
  else
    seed lib1 "/opt/fcsim/lib-setup.sh 52:54:00:fc:00:02 0x2000525400fc0001"
  fi
else
  # MAC：52:54:00:fc:<ファブリック 01=A・02=B>:<ノード 01=srv1・02=lib1・03=lib2>
  # ライブラリの LIO は両方のサーバを許す（どのサーバに見せるかはスイッチのゾーニングで決める）
  INIS="0x2000525400fc0101 0x2000525400fc0201 0x2000525400fc0104 0x2000525400fc0204"
  seed srv1 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:01:01,52:54:00:fc:02:01"
  seed srv2 "/opt/fcsim/fcoe-up.sh 52:54:00:fc:01:04,52:54:00:fc:02:04"
  # 管理 API（REST :8080・SNMP :161）。トラップは監視局 mon（172.30.31.30）と Zabbix のトラップ受信（.43:1162）へ
  seed lib1 "/opt/fcsim/lib-setup.sh -i 1 52:54:00:fc:01:02,52:54:00:fc:02:02 $INIS" "-traps 172.30.31.30:162,172.30.31.43:1162"
  seed lib2 "/opt/fcsim/lib-setup.sh -i 2 52:54:00:fc:01:03,52:54:00:fc:02:03 $INIS" "-traps 172.30.31.30:162,172.30.31.43:1162"
fi
echo "用意できた。sudo containerlab deploy -t lab/clab/$OUT"
