# 障害訓練のシナリオ

冗長構成（`lab/clab/fc-tape-ha.clab.yml`）で起こせる障害と、そのときに何が見えるかのまとめ。台本は `tools/drill.sh`。

## 構成（おさらい）

```
            ┌──── fab-a（FC スイッチ A、ドメイン 1）────┐
   srv1 ────┤   lib1（ライブラリ 1）・lib2（ライブラリ 2）  │   どの VM も FC の NIC を 2 つ持ち、A と B の両方につなぐ
   srv2 ────┴──── fab-b（FC スイッチ B、ドメイン 2）────┘
```

- サーバ srv1 からは、ライブラリ 2 台 × 装置 5 個（ロボット 1・ドライブ 4）× 経路 2 本 = **20 個**の SCSI 装置が見える。
  同じ装置が経路ごとに別の `/dev/sg`・`/dev/nst` になる（テープは dm-multipath を使わない）。
- 監視：Zabbix（http://172.30.31.42:8080/、Admin / zabbix）が、ライブラリを SNMP・REST・トラップで、スイッチを SNMP・REST・トラップで見る。
  監視局 mon（`sudo docker logs clab-fctape-ha-mon`）にもトラップが届く。

## 使い方

```
tools/drill.sh list                       # 一覧
tools/drill.sh status                     # srv1 の装置の数・Zabbix の障害
tools/drill.sh inject cable-lib1-a        # 起こす（手で訓練するとき）
tools/drill.sh recover cable-lib1-a       # 戻す
tools/drill.sh run cable-lib1-a           # 起こす → 確かめる → 戻す → 確かめる（OK・NG）
tools/drill.sh run all                    # 全部（約 25 分）
```

- 前提：冗長構成が動いている（`lab/clab/prepare.sh ha` と `sudo containerlab deploy -t lab/clab/run-ha.clab.yml`）。
  Zabbix の障害も確かめるなら `lab/clab/zabbix.sh up` と `python3 tests/zabbix-setup.py`（Zabbix が無ければ、装置の数だけ確かめる）。
- `run` は、経路が切れてから装置を消すまでの時間（`dev_loss_tmo`）を 10 秒にする。Linux の既定は 60 秒なので、手で訓練するときは、
  装置が消えるのが下の表より約 50 秒遅い。
- `run` は始める前に srv1 の装置が 20 個かを確かめる。前のシナリオが戻っていないと先へ進まない。

## シナリオ

時間は 2026-10-23 に `run all` で測ったもの（Zabbix の障害は、項目の取得の間隔が 10 秒）。

| 名前 | 起きること | 起こし方 | srv1 の装置 | Zabbix の障害 | 戻し方・戻るまで |
|---|---|---|---|---|---|
| `switch-a-down` | FC スイッチ A の停止（電源断・固まった） | `docker pause` | 20 → 10（経路 B だけ）約 25 秒 | 「fab-a: REST not responding」「fab-a: SNMP not responding」（データが 1 分途絶えて） | `docker unpause`。約 15 秒で 20 |
| `switch-b-down` | FC スイッチ B の停止 | 同上 | 20 → 10 | fab-b の同じもの | 同上 |
| `cable-srv1-a` | サーバ srv1 とスイッチ A の間のケーブル断 | srv1 のコンテナの eth1 を down | 20 → 10 約 30 秒 | 「fab-a port 0 (eth1) went down (SNMP trap)」「… is Offline」すぐ | eth1 を up。約 10 秒で 20 |
| `cable-lib1-a` | ライブラリ 1 とスイッチ A の間のケーブル断 | lib1 のコンテナの eth1 を down | 20 → 15（ライブラリ 1 の経路 A が消える）約 15 秒 | 「fab-a port 1 (eth2) went down (SNMP trap)」「… is Offline」すぐ | 約 10 秒で 20 |
| `port-lib2-b` | スイッチ B のライブラリ 2 のポートの故障・誤って無効 | スイッチの API で無効 | 20 → 15 約 15 秒 | 「fab-b port 2 (eth3) went down (SNMP trap)」「… is Disabled」すぐ | 有効に戻す。約 10 秒 |
| `zone-srv1-lib1` | ゾーンの誤り（srv1 とライブラリ 1 のゾーンを両方のファブリックから消す） | スイッチの API でゾーンを入れ替え | 20 → 10（ライブラリ 1 が見えない）約 15 秒 | 「fab-a / fab-b: zoning changed (SNMP trap)」（お知らせ、10 分） | 元のゾーンに戻す。数秒 |
| `lib1-offline` | ライブラリ 1 のオフライン（ロボットが命令を受けない） | libmgr の API | 20 のまま。srv1 からロボットの命令が Not Ready | 「lib1 went offline (SNMP trap)」「lib1 is not online (SNMP)」「(REST)」数秒 | online に戻す。数秒 |
| `lib2-tapealert` | ライブラリ 2 のドライブ 1 にクリーニングが要る（TapeAlert 20） | libmgr の API | 20 のまま。srv1 の TapeAlert のログ（`sg_logs -p 0x2e`）に Cleaning required | 「Drive 1 (LIB2_A1) on lib2: TapeAlert 20」「… TapeAlert raised (SNMP trap)」約 10 秒 | フラグを消す。数秒 |
| `lib1-poweroff` | ライブラリ 1 の電源断 | Redfish の ForceOff | 20 → 10 約 30 秒 | 「lib1: SNMP not responding」（データが 1 分途絶えて） | Redfish の On。On から FC の設定が終わるまで約 40〜60 秒（ssh が届くまで約 25 秒、FC の設定に 12〜33 秒）、20 に戻る |
| `srv1-reboot` | サーバ srv1 の再起動 | Redfish の ForceRestart | 起動の間は見えない。起動後 20 | 無し（下の「模擬の限り」） | ひとりでに戻る。起こしてから約 15 秒で、両方のスイッチに srv1 のログインし直しの記録 |

## 模擬の限り（本物と違うところ）

- **時間**：FC の生存の確認は FCoE のキープアライブ（8 秒間隔）で、スイッチは 20 秒途絶えるとログアウトさせる。本物の FC のリンクの断の検知（ミリ秒）よりずっと遅い。
- **電源断からの戻り**（2026-10-27、lib1-poweroff と同じ手順で 3 回ずつ測った。On の要求から、VM の中の `/var/log/fcsim-setup.rc` ができるまで）：
  qemu-bmc 0.10.0 で 59.4・38.1・59.2 秒、0.9 で 59.5・38.8・58.7 秒（版による違いは無い）。ssh が届くまでは 25〜27 秒でそろっていて、
  ばらつきは VM の中の FC の設定（fcsim-setup.service）にかかる時間（12 秒か 32〜34 秒）による。
- **srv1-reboot の確かめ**は、起こす前の時刻を残し、それより後の srv1 のログインの記録だけを見る（前の訓練の記録では成り立たない）。
  ケーブル断はリンクの状態（1 秒ごとに見る）で、すぐに分かる。
- **サーバの再起動でリンクが落ちない**：VM のリセットでは、コンテナの側のリンク（TAP・veth）は落ちない。サーバはリセットの数秒後に同じ WWPN でログインし直すので、
  スイッチのポートは Online のまま（本物なら光が途絶えてポートが落ちる）。スイッチの記録に「同じポートで FLOGI し直した」が残る。
- **スイッチの停止**は `docker pause`（凍らせる）でまねる。`docker stop`・`restart` はリンク（veth）ごと消え、ContainerLab が作り直さないので使わない（構成の作り直しが要る）。
- **ドライブ・ロボットの故障**は TapeAlert のフラグとオフラインだけ。テープが詰まる・読めない（媒体のエラー）は模擬していない。
- テープを使う側の切り替え（経路 A が落ちたら経路 B のドライブを使う）は、アプリの仕事。この訓練は、装置が消える・戻ることまでを確かめる。

## 訓練で見つかって直したこと

- **ケーブル断でスイッチが落ちていた**：ポートのリンクが落ちると、スイッチ（fcsw）が受け取りのエラーで終わり、Docker が起こし直したコンテナにはリンクが無かった
  （スイッチ全体が止まる）。リンクが落ちても止まらず、そのポートを Offline にするように直した。
- **再起動・電源断のあと FC がつながらなかった**：VM の FC の設定（MTU・スキャッタギャザー・LIO・mhVTL・FCoE の順番）は最初の起動でしか流れていなかった。
  起動のたびに流す systemd の `fcsim-setup.service` にした。
