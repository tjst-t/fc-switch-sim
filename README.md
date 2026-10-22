# fc-switch-sim

FC スイッチ（FCoE の FCF）のシミュレーター。仮想マシンの Linux から見て「FC の HBA でファブリックにつながっている」状態を作り、
ゾーニング・ポートの無効化・RSCN などのスイッチの動きを再現する。IT インフラのデジタルツイン（サーバ・FC 接続のテープライブラリ）の部品。

- ベンダーに依らない形で作る（ベンダーの CLI・API の「皮」は後でかぶせる）。
- 既存の FCF の実装（ipxe/fcoed など）は使わず、規格（FC-BB-5/6 の FIP、FC-LS、FC-GS）と Linux の振る舞いから作っている。

## できること

| 機能 | 中身 |
|---|---|
| FIP | 発見の要求への広告（ENode の最大の長さまで詰める＝経路の MTU を確かめる）、定期の広告、キープアライブの見張り、Clear Virtual Link |
| ログイン | FLOGI・FDISC（NPIV）・LOGO、FC_ID の割り当て（Domain・ポート番号・ALPA）、FPMA の MAC |
| ファブリックのサービス | ネームサーバ（RFT_ID・RFF_ID・RNN_ID・RSPN_ID・RSNN_NN・GPN_FT・GID_FT・GNN_FT・GPN_ID・GNN_ID・GFF_ID・GFT_ID・GID_PN など）、SCR、RSCN、ECHO、ABTS |
| 転送 | ログインした VN_Port どうしの FCoE のフレームを、宛先の FC_ID で転送する（送り元の MAC と FC_ID を確かめる） |
| ゾーニング | WWPN とエイリアスで書く。ネームサーバの答えを絞り、ゾーンの外へのフレームは捨てる（ハードゾーニング）。変えると RSCN |
| ポート | 有効・無効（無効にするとログインを Clear Virtual Link で切る） |
| 復旧 | ログインしていない VN_Port からフレームやキープアライブが届いたら、Clear Virtual Link でやり直しを求める（スイッチの再起動のあと、ポートを有効に戻したあと） |
| API | HTTP の JSON（`sim/api/api.go` の頭に一覧） |

## 構成

```
cmd/fcsw/        スイッチのプログラム（fcsw -config switch.yaml -listen :8080）
sim/fc/          FC のヘッダ・ELS・CT・FCoE の包み方
sim/fip/         FIP の読み書き
sim/fabric/      スイッチの本体（ログイン・ネームサーバ・転送・ゾーニング・RSCN）と単体の試験
sim/rawport/     Linux のインタフェースで生の Ethernet を送受信する（AF_PACKET）
sim/api/         HTTP の API
lab/images/      VM のイメージ（Debian 13 ＋ fcoe-utils・LIO・mhVTL）と、VM の中で流すスクリプト
lab/direct/      直結の試験の場：VM 2 台を TAP でスイッチにつなぐ（ContainerLab を使わない最小の形）
```

## 直結の試験の場で確かめたこと（2026-10-22、Debian 13・カーネル 6.12）

構成：サーバ VM（イニシエータ）── TAP ── fcsw ── TAP ── テープライブラリ VM（mhVTL の STK L700 ＋ LTO ドライブ 4 台を LIO の tcm_fc で公開）

- Linux のソフトウェア FCoE（fcoe-utils、ファブリックモード）が fcsw を FCF として選び、FLOGI で FC_ID を受け取った。`/sys/class/fc_host` に WWPN・FC_ID・ファブリック名が出る。
- ネームサーバへの登録・SCR・GPN_FT による発見・PLOGI・PRLI が通り、サーバ VM にライブラリのロボット（`/dev/sch*`・`/dev/sg*`）と LTO ドライブ（`/dev/st*`・`/dev/nst*`）が現れた。
- `mtx` でテープの出し入れ、`mt`・`dd` で書き込みと読み戻し：ブロック 256KB・1MB・2MB で書いた中身と読み戻した中身が一致（120〜250MB/s）。
- ゾーンから外すと、RSCN でサーバ側の経路が Blocked になり、`dev_loss_tmo`（60 秒）のあと装置が消える。ゾーンを戻すと 10 秒以内に戻る。
- 経路の MTU が 1500 だとログインできない（FCF の広告が通らない）。MTU 9000 で通る。

### 分かった注意点

- **経路の MTU を上げること**（FCoE は 1 フレーム 2158 バイト）。TAP・ブリッジ・VM の NIC（virtio-net の `host_mtu`）・VM の中の NIC の全部。
- **ライブラリ側は、ターゲットを作ってから FCoE を有効にすること。** 先に FCoE でログインすると、相手の libfc がイニシエータとしての役割を覚えたままになり、あとでターゲットにしても PLOGI を「役割が合わない」と断る（相手側で `fcoeadm -r` すれば直る）。`targetcli` は fc_host ができるまで tcm_fc を見せないので、rtslib を直に使う（`lab/images/guest/lib-setup.sh`）。
- **FC の NIC のスキャッタギャザーを切ること**（`ethtool -K <NIC> sg off`）。切らないと、テープへの 1MB 以上のブロックの書き込みでイニシエータのカーネルが落ちた（Bad page state → panic）。
- **mhVTL はリリース版（1.8-0_release）を使い、モジュールの `SCSI_MAX_SG_CHAIN_SEGMENTS` を 1024 に直すこと。** 既定（128 個・4KB 境界＝512KB）では LIO の pscsi が 1MB のブロックを組めない（`pSCSI: failed to append bio`）。master は 2026-10-21 の時点でリンクに失敗した。
- 使用中のターゲットの相手が突然いなくなると、tcm_fc が終わらないコマンドを待って固まることがある（`ft_prlo` → `target_wait_for_cmds`）。そのときは VM を起こし直すしかない。
- mhVTL の仮想テープの容量は既定で 500MB（`/etc/mhvtl/mhvtl.conf` の `CAPACITY`）。
- `st` は書き込みのあと閉じるときにファイルマークを書く。`mt weof` を重ねると空のファイルが挟まる。

## ContainerLab の構成（lab/clab/fc-tape.clab.yml）

```
srv1（qemu-bmc の VM）eth1 ──── eth1 fab-a（fcsw のコンテナ）eth2 ──── eth1 lib1（qemu-bmc の VM、mhVTL）
```

- VM は `ghcr.io/tjst-t/qemu-bmc:0.8`。Redfish・IPMI で電源を操作できる（`https://172.30.30.21/redfish/v1`、admin / password）。
- VM は cloud-init で起動時に FC の設定まで済ませる（サーバ：`fcoe-up.sh`、ライブラリ：`lib-setup.sh`）。
- VM への ssh：`ssh -i .cache/id_lab lab@172.30.30.21`（srv1）・`.22`（lib1）。スイッチの API：`http://172.30.30.10:8080/api/v1/`。
- **回避策：** qemu-bmc 0.8 は VM の NIC の TAP・ブリッジの MTU を 1500 のままにするので、exec で 9500 に上げている。qemu-bmc 0.9 の `VM_NET_MTU` が出たら置き換える。

```
lab/images/build-base.sh                         # 共通イメージ（初回だけ）
lab/clab/prepare.sh                              # スイッチのイメージ・VM のディスクと cloud-init の種
sudo containerlab deploy -t lab/clab/run.clab.yml
tests/e2e.sh                                     # 通しの試験（--deploy で作り直しから、--destroy で最後に壊す）
sudo containerlab destroy -t lab/clab/run.clab.yml --cleanup
```

通しの試験（`tests/e2e.sh`）が確かめること：ログインと FC_ID、ロボットとドライブ 4 台が見える、テープの読み書き（256KB・1MB・2MB で一致）、ゾーンから外すと消え戻すと戻る、ポートを無効にすると消え有効にすると戻る、Redfish で電源の状態が読める。
2026-10-22 に、作り直しから 1 回と、同じ構成のまま 2 回、全部 OK。

## 動かし方（直結の試験の場）

```
lab/images/build-base.sh          # VM の共通イメージを作る（.cache/fcsim-base.qcow2、KVM が要る）
lab/direct/up.sh                  # TAP・fcsw・VM 2 台を起こす
# ライブラリ VM の中で：lib-setup.sh ens4 <サーバの WWPN>
# サーバ VM の中で：fcoe-up.sh ens4
lab/direct/down.sh
```

API の例：

```
curl localhost:8080/api/v1/nameserver
curl -X PUT localhost:8080/api/v1/zoning -d '{"defaultZone":"noaccess","zones":[{"name":"z1","members":["20:00:52:54:00:fc:00:01","20:00:52:54:00:fc:00:02"]}]}'
curl -X POST localhost:8080/api/v1/ports/1/disable
```

## これから

- qemu-bmc 0.9 の `VM_NET_MTU` に切り替え、MTU の回避策を外す。
- 複数のサーバ・ライブラリ、ファブリック A と B の冗長構成。
- ライブラリの管理 API（SNMP など）の模擬、ベンダーの CLI・API の皮。
