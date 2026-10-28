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


## 初期の資格情報（ラボ専用）

ラボの資格情報は、どれも試すための固定の値。**外に出る環境（ラボの管理網の外から届くところ）で使う前に、必ず変えること。**

| もの | 既定 | 変え方 |
|---|---|---|
| BMC（qemu-bmc の Redfish・IPMI） | admin / password | `lab/clab/*.clab.yml` の `IPMI_USER`・`IPMI_PASS` |
| Zabbix の Web・API | Admin / zabbix | Zabbix で変え、`tests/zabbix-setup.py` などには環境変数 `ZBX_ADMIN_USER`・`ZBX_ADMIN_PASSWORD` で渡す |
| Zabbix の DB（PostgreSQL） | zabbix / zabbix | 環境変数 `ZBX_DB_PASSWORD`（`lab/clab/zabbix.sh up` のとき） |
| VM のユーザ | lab / lab（sudo はパスワード無し） | `lab/images/base/user-data` |
| SNMP のコミュニティ | public | スイッチの設定（`snmp.community`）・`lab/images/guest/libmgr/snmpd.conf` |

VM への ssh の鍵（`.cache/id_lab`）は手元で作るもので、リポジトリには入れない。

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

- VM は `ghcr.io/tjst-t/qemu-bmc:0.11.0`（digest で固定）。FC の NIC の MTU は `VM_NET_MTU: "9500"`（TAP・ブリッジ・virtio-net の host_mtu をそろえて上げる）。Redfish・IPMI で電源を操作できる（`https://172.30.30.21/redfish/v1`、admin / password）。
- VM は cloud-init で起動時に FC の設定まで済ませる（サーバ：`fcoe-up.sh`、ライブラリ：`lib-setup.sh`）。
- VM への ssh：`ssh -i .cache/id_lab lab@172.30.30.21`（srv1）・`.22`（lib1）。スイッチの API：`http://172.30.30.10:8080/api/v1/`。

```
lab/images/build-base.sh                         # 共通イメージ（初回だけ）
lab/clab/prepare.sh                              # スイッチのイメージ・VM のディスクと cloud-init の種
sudo containerlab deploy -t lab/clab/run.clab.yml
tests/e2e.sh                                     # 通しの試験（--deploy で作り直しから、--destroy で最後に壊す）
sudo containerlab destroy -t lab/clab/run.clab.yml --cleanup
```

通しの試験（`tests/e2e.sh`）が確かめること：ログインと FC_ID、ロボットとドライブ 4 台が見える、テープの読み書き（256KB・1MB・2MB で一致）、ゾーンから外すと消え戻すと戻る、ポートを無効にすると消え有効にすると戻る、Redfish で電源の状態が読める。
2026-10-22、qemu-bmc 0.9 で：作り直しから 5 回、同じ構成のまま 2 回、全部 OK。
さらに、失敗しやすい条件（`E2E_NO_RESET=1`：試験の最初にゾーンを入れ直さない）で、下の修正のあと作り直しから 10 回、全部 OK（修正前は 5 回中 3 回失敗）。

### 直した不具合：ログインの RSCN が早すぎて、相手に発見されない

スイッチは FLOGI の時点で RSCN を送っていたが、そのときはまだ相手が FC-4 の種類（FCP）を登録していないので、
RSCN を受けた側の GPN_FT に出てこず、発見されなかった。代わりに相手からの PLOGI で経路ができ、
Linux（libfc）は自分で発見していない経路をゾーンの変更のあとの再発見で消さないため、ゾーンから外しても装置が残った。
いまは RFT_ID・RFF_ID で登録が変わったときにも RSCN を送る（本物のスイッチと同じ）。

調査用に、`FCSIM_DEBUG=1 lab/clab/prepare.sh` で VM の libfc・libfcoe のデバッグのログを起動時から出せる。

## 冗長構成（lab/clab/fc-tape-ha.clab.yml）

```
            ┌──── fab-a（ドメイン 1）────┐
   srv1 ────┤        lib1・lib2          │   srv1・srv2・lib1・lib2 はどれも FC の NIC を 2 つ持ち、
   srv2 ────┴──── fab-b（ドメイン 2）────┘   A と B の両方につなぐ
```

- サーバ 2 台・テープライブラリ 2 台・スイッチ 2 台。管理網は 172.30.31.0/24（スイッチ A .10・B .11、srv1 .21・lib1 .22・lib2 .23・srv2 .24）。
- ゾーン：srv1 はライブラリ 1 と 2、srv2 はライブラリ 2 だけ。ライブラリの LIO は両方のサーバを許し、どのサーバに見せるかはゾーニングで決める。
- ドライブ 1・2 は LTO-8、3・4 は LTO-6。テープは LTO-8 なので、3・4 に入れようとするとライブラリが断る（本物と同じ）。
- ライブラリは `lib-setup.sh -i <番号>` で、シリアル番号（LIB1_A・LIB2_A…）・NAA・テープのバーコード（E11…・E21…）を分ける。
  NIC（ファブリック）ごとに LIO のターゲットを作り、同じ装置を両方の経路から見せる。
- サーバからは、ライブラリ 2 台 × 装置 5 個 × 経路 2 本 = 20 個の SCSI 装置が見える（同じ装置が 2 本の経路でそれぞれ別の `/dev/sg`・`/dev/nst` になる。テープは dm-multipath を使わないので、経路の使い分けはアプリかドライバの仕事）。
- スイッチの障害は `docker pause`（コンテナを凍らせる＝電源断・固まった状態）でまねる。`docker stop` はリンク（veth）ごと消えて ContainerLab が作り直さないので使わない。

```
lab/clab/prepare.sh ha
sudo containerlab deploy -t lab/clab/run-ha.clab.yml
tests/e2e-ha.sh            # --deploy で作り直しから
```

通しの試験（`tests/e2e-ha.sh`）が確かめること：
1. 6 つの N_Port がログインし、サーバの fc_host が A・B とも Online
2. 装置 20 個、シリアル番号 10 種類がそれぞれ経路 A と B で見える
3. 両方のライブラリで、経路 A のロボットでテープを入れ、経路 A のドライブで書き、経路 B の同じドライブで読み戻して一致
4. 経路 B で 400MB を書いている最中にスイッチ A を止めても、書き込みは成功し中身も一致。経路 A の装置は約 20 秒で消え、経路 B の 10 個は残る
5. スイッチ A を戻すと約 10 秒で 20 個に戻る
6. スイッチ B でライブラリ 2 のポートだけを無効にすると、ライブラリ 2 の経路 B の 5 個だけが消え、戻すと戻る
7. srv2 にはライブラリ 2 だけ（10 個）が見える。srv1 と srv2 から別々のライブラリへ同時に 200MB 書いて一致。スイッチ A のゾーンに srv2 とライブラリ 1 を足すと srv2 にライブラリ 1 の経路 A（5 個）が増え、戻すと消える（srv1 は 20 個のまま）

2026-10-22 に、サーバ 2 台の構成で作り直しから 2 回とそのまま 1 回、全部 OK。
試験では経路が切れてから装置を消すまでの時間（`dev_loss_tmo`）を 10 秒にしている（Linux の既定は 60 秒）。

## テープライブラリの管理 API（libmgr）

モニタリングツールが使う口の模擬。ベンダーに依らない形（REST・SNMP・SNMP トラップ）。冗長構成（fc-tape-ha）のライブラリの VM に入っている。

- `libmgr`（`cmd/libmgr`・`sim/libmgr`）はライブラリの VM の中で動き、mhVTL の状態を 2 秒ごとに読む（ロボットへの READ ELEMENT STATUS と TEST UNIT READY を `sg_raw` で）。
  サーバが FC 越しにテープを出し入れすると、それがそのまま管理 API に出る。
- REST：`http://172.30.31.22:8080/api/v1/`（lib1）・`.23`（lib2）

  | | |
  |---|---|
  | `GET /library`・`/drives`・`/slots`・`/events` | 全体・ドライブ（状態・テープ・TapeAlert・入れた回数）・スロット・起きたこと |
  | `POST /library/offline`・`/online` | オフラインにする（サーバからのロボットの命令が Not Ready になる）・戻す |
  | `PUT /drives/{n}/tapealert` `{"flags":[15]}` | TapeAlert のフラグを立てる（`[]` で消す）。サーバも TapeAlert のログ（`sg_logs -p 0x2e`）で読める |
  | `POST /import` `{"barcode":"IMP001L8"}` | 新しいテープを作ってメールスロットに入れる（オペレータの差し込み） |

- SNMP（v2c、コミュニティ public）：udp 161。MIB-II（sysName など）と `FCSIM-TAPE-LIBRARY-MIB`（`docs/`、根は `.1.3.6.1.4.1.99999.1`、99999 は仮の企業番号）。
  snmpd の pass_persist で `libmgr snmp-pass` が答える。値は最大 1 秒古い。
- トラップ（v2c）：監視局 `mon`（172.30.31.30:162）へ。`fsLibStatusChange`・`fsDriveLoad`・`fsDriveUnload`・`fsDriveAlert`・`fsIEImport`・`fsIERemove`。
  `sudo docker logs clab-fctape-ha-mon` で 1 行ずつ読める。
- mhVTL 1.8 の `vtlcmd ... load map` は新しい形式のテープ（`data.0`）を見つけられないので、`libmgr` が空の `data` を置いてから入れている。

通しの試験（`tests/e2e-libmgr.sh`）：REST と SNMP が一致する、サーバのテープの出し入れが SNMP とトラップに出る、オフラインでサーバの命令が通らなくなりトラップが出る、
TapeAlert が SNMP・トラップ・サーバの TapeAlert のログに出る、投入したテープがメールスロットに入りサーバから見えて、保管スロットへ移すとトラップが出る、ライブラリ 2 からもトラップが届く。

## チェンジャーの障害注入（試験用の口 `/_sim/v1`）

テープを使うアプリが、ロボットの異常（MOVE MEDIUM の失敗・扉が開いている・扉を閉じたあとの UNIT ATTENTION）をどう扱うかを試すための、
汎用の障害注入。サーバから FC 越しに見える SCSI の答え（データプレーン）だけを変え、管理 API（`/api/v1`）・SNMP・トラップには出さない
（障害の間もライブラリは online のまま）。mhVTL のパッチ `0003`（`lab/images/base/patches/README.md`）と libmgr で作っている。

- 口：`http://172.30.31.22:8080/_sim/v1/`（lib1）・`.23`（lib2）

  | | |
  |---|---|
  | `GET /state` | いまの障害・扉・待っている UNIT ATTENTION・BECOMING READY の残りの秒数 |
  | `PUT /faults/move-medium` `{"key":4,"asc":"44","ascq":0,"direction":"any"}` | MOVE MEDIUM と INITIALIZE ELEMENT STATUS（07h・E7h）を、この sense で失敗させる（数は 10 進、文字列は 16 進）。`direction` は `any`・`load`（ドライブへ入れる移動だけ）・`unload`（ドライブから出す移動だけ）。READ ELEMENT STATUS は実際の在庫を返す |
  | `DELETE /faults/move-medium` | 障害を消す |
  | `POST /door/open` `{"notReady":false}` | 扉を開ける。ロボットを動かす命令は NOT READY 04h/03h。`notReady:true` なら TEST UNIT READY・READ ELEMENT STATUS も |
  | `POST /door/close` `{"clearFaults":true,"becomingReadySeconds":0}` | 扉を閉じる。次のチェンジャーへの命令（どの経路・どのサーバからでも、全体で1回）に UNIT ATTENTION 28h/00h。既定で障害も消す。`becomingReadySeconds` の間は先に NOT READY 04h/01h |

- 答えはどれも `GET /state` と同じ形。状態はライブラリの VM の mhVTL のメモリにだけあり、VM を起こし直すと消える（入れ直すのは呼ぶ側）。
- UNIT ATTENTION は当面チェンジャーだけで、経路ごとではなく全体で1回（経路ごとに出すには LIO の target_core_mod に手を入れる必要がある）。
  ドライブ（vtltape）には出さない。
- libmgr の見回り（TEST UNIT READY・READ ELEMENT STATUS）は CDB の CONTROL バイトのベンダー固有のビットを立てて送り、mhVTL はその命令に
  28h/00h・04h/01h・扉の NOT READY を返さない（見回りが UNIT ATTENTION を先に取ってしまわないため）。READ ELEMENT STATUS の返り値は
  libmgr が自分で読む（`mtx` は使わない。番号の振り方は `mtx status` と同じで、ラボの VM（mhVTL）から採った見本との突き合わせを単体の試験にしている）。
- ライブラリの VM の中では `vtlcmd 10 fault …`・`vtlcmd 10 door …` でも同じことができる。

通しの試験（`tests/e2e-sim-faults.sh`、ライブラリ 1）：障害で MOVE MEDIUM・INITIALIZE ELEMENT STATUS が指定の sense で失敗し在庫は読める、`direction`、
扉が開いている間の 04h/03h、閉じたあとの 28h/00h が全体で1回（見回りに取られず、別の経路は GOOD）、`clearFaults`・`becomingReadySeconds`、
その間 REST・SNMP が online のままで状態の変化の出来事も Zabbix の lib1 の障害も無いこと。最後に元に戻す。

## Zabbix での監視

冗長構成（fctape-ha）の管理網に Zabbix 7.0 LTS（公式のコンテナ、PostgreSQL）を足し、ライブラリとスイッチを監視する。

```
lab/clab/zabbix.sh up            # DB .40・サーバ .41・Web .42（http://172.30.31.42:8080/、Admin / zabbix）
python3 tests/zabbix-setup.py    # テンプレート・ホストを Zabbix の API で入れる（何度流してもよい）
tests/e2e-zabbix.sh              # 障害を起こして確かめる（--setup で上の 2 つも流す）
lab/clab/zabbix.sh down
```

- Zabbix は ContainerLab の外の docker のコンテナとして、同じ管理網（fctape-ha-mgmt）に載せる。
  動いている構成に ContainerLab でノードを足すと、VM まで作り直しになるため。
- テンプレート（`Templates/FCSIM`）：

  | テンプレート | 読み方 | 主な項目と障害 |
  |---|---|---|
  | FCSIM Tape Library SNMP | SNMP v2c（FCSIM-TAPE-LIBRARY-MIB） | 状態（not online で「高」）、SNMP が 1 分無応答、ドライブの発見（状態・テープ・TapeAlert・入れた回数）、TapeAlert があれば「警告」 |
  | FCSIM Tape Library REST | HTTP エージェントで libmgr の `/api/v1/library` | 状態（not online で「高」）、テープの入ったドライブの数、メールスロットのテープ（あれば「情報」） |
  | FCSIM FC Switch REST | HTTP エージェントで fcsw の `/api/v1/ports`・`/nameserver` | ログインの数、REST が 1 分無応答（「高」）、ポートの発見（状態が Online でなければ「警告」・ゾーニングで捨てた数） |

- ホスト：lib1・lib2（SNMP と REST の両方）、fab-a・fab-b（REST）。既定の「Zabbix server」ホストは消す（エージェントが無いため）。
- Zabbix の JSONPath は `true` との比較を扱えず、一致が無いと失敗するので、数える項目は前処理の JavaScript で数えている。

通しの試験（`tests/e2e-zabbix.sh`）で確かめること（どれも障害に上がり、戻すと消える）：ライブラリ 1 のオフライン（SNMP と REST の両方）、
ライブラリ 2 のドライブ 3 の TapeAlert、メールスロットへのテープの投入（サーバが保管スロットへ移すと消える）、スイッチ A のポートの無効化、
スイッチ B の停止（`docker pause`、約 1 分で上がる）、サーバがテープを入れるとドライブの値が変わる。2026-10-22 に Zabbix を作り直してから 2 回、全部 OK。
スイッチは REST に加えて SNMP とトラップでも監視する（下の「FC スイッチの SNMP」）。

### SNMP トラップ

- Zabbix 公式のトラップ受信（`zabbix-snmptraps`、172.30.31.43:1162）が受け、共有のボリュームのファイルで Zabbix サーバに渡す（`ZBX_ENABLE_SNMP_TRAPS=true`）。
  ライブラリの libmgr は、監視局 mon と、このトラップ受信の両方へ送る。トラップは送り元の IP がホストの SNMP のインタフェースと一致するホストに入る。
- テンプレート `FCSIM Tape Library Traps`：トラップの本文を正規表現で選ぶ項目（状態の変化・TapeAlert・出し入れとメールスロット・どれにも当たらないもの）。
  - 「went offline (SNMP trap)」：offline のトラップで立ち、online のトラップで消える（回復の条件を別に持つ）。
  - 「Drive N ... TapeAlert raised (SNMP trap)」：SNMP でドライブを発見し、そのドライブの番号を含むトラップを受ける項目をドライブごとに作る。
    フラグのあるトラップで立ち、空のトラップで消える。同じドライブのテープの出し入れのトラップでは変わらない。
  - 知らないトラップは `snmptrap.fallback` に入り、10 分間「unmatched SNMP trap received」（情報）。
- トリガーの式の文字列の中では、正規表現の `\` と `"` をさらに `\\` と `\"` で書く（`tests/zabbix-setup.py` の `zstr`）。
- 試験（`tests/e2e-zabbix.sh` の 8）：2026-10-22 に Zabbix を作り直してから 2 回、全部 OK。
- 試験で投入したテープは `tests/lib-cleanup.sh` でライブラリから出す（試験の最初と最後に流す）。出さないと保管スロットがあふれ、テープを移せなくなる。

## FC スイッチの SNMP

`fcsw` は設定のファイルに `snmp:` があると、SNMPv2c（get・getnext・getbulk、udp 161）で答え、トラップを送る（`sim/snmpagent`・`sim/swsnmp`）。

```yaml
snmp:
  listen: ":161"
  community: public
  traps: ["172.30.31.30:162", "172.30.31.43:1162"]   # 監視局 mon と Zabbix のトラップ受信
```

- MIB は `docs/FCSIM-FC-SWITCH-MIB.txt`（根 `.1.3.6.1.4.1.99999.2`、仮の企業番号）。MIB-II の system（sysName など）、スイッチ（名前・WWN・ドメイン・ログインの数・ゾーンの数・ゾーンの変更の回数）、
  ポートの表（状態 online・noLogin・disabled、FC_ID、ゾーニングで捨てた数、FCoE のフレームの数）、ログインの表（FC_ID・WWPN・ポート・NPIV）。
- トラップ：`fswPortStateChange`（ポートの状態が変わった）・`fswLogin`・`fswLogout`・`fswZoningChange`。
- Zabbix のテンプレート：`FCSIM FC Switch SNMP`（ログインの数・ゾーン・ポートの発見と状態、SNMP の無応答）と `FCSIM FC Switch Traps`
  （ポートごとの「went down (SNMP trap)」：noLogin・disabled のトラップで立ち online のトラップで消える、ログイン・ログアウトのトラップ、
  「zoning changed (SNMP trap)」はお知らせで 10 分）。fab-a・fab-b のホストは SNMP のインタフェースにした。
- 試験（`tests/e2e-zabbix.sh` の 9）：2026-10-22 に Zabbix を作り直してから 2 回、全部 OK。FC の冗長・1 系統・管理 API の試験も、この版で全部 OK。

## 障害訓練

冗長構成で起こせる 10 の障害（スイッチの停止・ケーブル断・ポートの無効・ゾーンの誤り・ライブラリのオフライン・TapeAlert・ライブラリの電源断・サーバの再起動）と、
そのときサーバと Zabbix に何が見えるかは `docs/failure-drills.md`。台本は `tools/drill.sh`（`list`・`inject`・`recover`・`run <名前>|all`）。
2026-10-23 に `tools/drill.sh run all` が全部 OK。

## テープのロード・巻き戻し・位置決め（シーク）の時間

mhVTL には、ドライブごとにロード・取り出し・巻き戻し・位置決め（locate・space）の遅延を入れる仕組みが元から入っている
（`vtlcmd <ドライブの ID> delay load|unload|rewind|position <秒>`）。ただし daemon（vtltape）の再起動で毎回消えるので、
冗長構成のライブラリ VM 起動時に `lib-setup.sh` が設定し直す（環境変数 `FCSIM_TAPE_DELAYS` が空でないときだけ。既定は無効）。

```
FCSIM_TAPE_DELAYS=1 lab/clab/prepare.sh ha
sudo containerlab deploy -t lab/clab/run-ha.clab.yml
```

- ドライブ 11・12（LTO-10）：ロード 12 秒・取り出し 10 秒・位置決め 8 秒・巻き戻し 20 秒。13・14（LTO-6）：ロード 10 秒・取り出し 8 秒・位置決め 6 秒・巻き戻し 15 秒
  （mhVTL の上限は load・unload・position が 20、rewind が 30。`man vtlcmd(1)`）。
- 効くのは実際にドライブへ読み書き・`mt rewind`・`mt fsf` 等をしたとき（ロード中・巻き戻し中はドライブが Not Ready になり、
  `mt status` のような問い合わせだけのコマンドはカーネル側のキャッシュを返すため変化が見えない）。
  2026-10-23、ラボの VM で `vtlcmd` で設定した秒数どおりにブロックされることを確認した（rewind 8 秒指定→実測 8.03 秒、
  position 6 秒→実測 6.10 秒、load 15 秒→読み出しが 15.57 秒ブロック）。
- 既定は無効：有効にすると読み書きのたびに数秒〜数十秒ブロックされ、既存の e2e テスト（`tests/e2e-ha.sh` 等）の
  タイムアウトや繰り返し回数の前提と噛み合わないため、デモ・手元での確認用に留める。

## 動かし方（直結の試験の場）

```
lab/images/build-base.sh          # VM の共通イメージを作る（.cache/fcsim-base.qcow2、KVM が要る。
                                  # FCSIM_BASE=<パス> で別の名前に。prepare.sh も同じ変数でそのイメージを使う。
                                  # 動いている VM のディスクは今のイメージの差分なので、作り直すときは上書きしない）
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

- スイッチの再起動（状態を失う）の模擬。
- ベンダーの CLI・API・MIB の皮（スイッチとライブラリ）。

## 外部のトポロジから使う

FC スイッチ（fcsw）・サーバの VM・テープライブラリの VM を、自分の containerlab のトポロジの部品として使える。
`lab/clab/prepare.sh seed` で、VM 1 台ぶんの種とディスクを、置き場・ノード名・MAC・管理アドレスを指定して作る。
コンテナへの bind、データ網の MTU（containerlab の既定 9500 に頼っている）、版の固定、状態を残したまま起こし直す口（`-K`・`FCSIM_KEEP_DISK=1`）、
single の構成に libmgr を入れる口（`FCSIM_LIBMGR=1`）は `docs/external-topology.md`。動く例は `lab/external/`。

## ライセンス

- このリポジトリは Apache License 2.0（直下の `LICENSE`）。
- ただし `lab/images/base/patches/` の mhVTL へのパッチは、upstream の mhVTL と同じ GPL-2.0。upstream から取ったもの（0001）も、
  このリポジトリで書いたもの（0002 以降）も mhVTL への変更なので GPL-2.0 とする。詳しくはそのディレクトリの `README.md`。
