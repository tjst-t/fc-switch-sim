# 外部のトポロジから部品として使う

このリポジトリの部品を、自分の containerlab のトポロジに組み込むときの約束事。

- FC スイッチのシミュレーター fcsw（コンテナイメージ）
- サーバの VM（FCoE のイニシエータ）
- テープライブラリの VM（mhVTL を LIO の tcm_fc で FC のターゲットにしたもの。必要なら管理 API の libmgr も）

VM はどれも [qemu-bmc](https://github.com/tjst-t/qemu-bmc) のコンテナの中で動く。
動く例は `lab/external/`（`make-example.sh`・`example.clab.yml`・`switch.yaml`）にある。lab/clab の構成とは、名前・MAC・管理網を変えてある。

## 1. 作るもの

```
lab/images/build-base.sh                     # 共通の VM イメージ（.cache/fcsim-base.qcow2）。KVM が要る。初回だけ
docker build -f image/Dockerfile -t fc-switch-sim/fcsw:dev .   # FC スイッチのイメージ
lab/clab/prepare.sh seed ...                 # VM 1 台ぶんの種（seed.iso）とディスク（disk.qcow2）。台数ぶん流す
```

- `build-base.sh` は `FCSIM_BASE=<パス>` で出力先を変えられ、`prepare.sh` も同じ変数でそのイメージを使う。
- 動いている VM のディスクは共通イメージの差分なので、共通イメージを作り直すときは上書きせず、別の名前で作る。
- `build-base.sh` は、VM の中の build が失敗すると非 0 で終わり、出力を置き換えない（失敗したものは `<出力>.failed` に残る）。
  ログは `.cache/base-build-console.log`（シリアルコンソール）の `FCSIM-BUILD-` の行。

## 2. `prepare.sh seed`：種とディスクだけを作る

```
lab/clab/prepare.sh seed -d <置き場> -n <ノード名> -r server|library -f <FC の NIC の MAC[,MAC]> [引数]
```

| 引数 | 意味 |
|---|---|
| `-d <置き場>` | `seed.iso`・`disk.qcow2`（と元の `user-data`・`meta-data`・`network-config`）を作るディレクトリ。`disk.qcow2` は作り直す |
| `-n <ノード名>` | VM のホスト名 |
| `-r server` | サーバ：起動のたびに `fcoe-up.sh <MAC…>` で FCoE（fabric モード）を上げる |
| `-r library` | テープライブラリ：起動のたびに `lib-setup.sh` で mhVTL・LIO のターゲット・FCoE を上げる |
| `-f <MAC[,MAC]>` | FC に使う NIC の MAC。qemu-bmc の `VM_MACS` と同じにする。WWPN は `20:00:<MAC>` になる |
| `-I "<WWPN> …"` | ライブラリ：LIO のターゲットに許すイニシエータ（`0x2000<サーバの MAC の 12 桁>` の形）。必須。どのサーバに見せるかは、スイッチのゾーニングで絞る |
| `-i <番号>` | ライブラリ：番号（既定 1）。シリアル番号 `LIB<番号>_A`・NAA・初期のテープのバーコードを分ける（同じファブリックに複数台置くとき） |
| `-L "<引数>"` | ライブラリ：管理 API（libmgr：REST :8080・SNMP :161）を入れ、この引数で動かす（例 `-L "-traps 10.0.0.30:162"`、引数なしは `-L ""`）。省くと入れない |
| `-m <MAC>` | 管理の NIC の MAC（既定 `52:54:00:aa:00:01`） |
| `-a <アドレス/長さ>` `-g <GW>` `-s <DNS>` | 管理の NIC を固定のアドレスにする。省くと DHCP |
| `-k <ファイル>` | VM のユーザ `lab` に入れる ssh の公開鍵（既定 `.cache/id_lab.pub`） |
| `-K` | 置き場に前に作った差分ディスクと種があれば、作り直さずにそのまま使う（下の「状態を残したまま起こし直す」） |

- libmgr を入れるときだけ libmgr を build する。ホストに Go が無ければ golang のコンテナ（digest で固定）の中で build する
  （`FCSIM_LIBMGR_BUILD=docker` でいつもコンテナで）。
- `FCSIM_TAPE_DELAYS=1`（テープのロード・巻き戻しの時間を入れる）・`FCSIM_DEBUG=1` も、`prepare.sh single|ha` と同じように効く。
- 引数を付けない `prepare.sh`・`prepare.sh single|ha` の動き（lab/clab の構成の `run.clab.yml` を作る）は変わらない。
- `prepare.sh single` は、既定ではライブラリに libmgr を入れない。`FCSIM_LIBMGR=1` を付けると入れ（引数は `FCSIM_LIBMGR_ARGS`）、
  `run.clab.yml` の lib1 の管理の NIC に 8080・161 の転送を足す。管理 API（`/api/v1`・SNMP）と障害注入の口（`/_sim/v1`）が使える。
  確かめ方：`FCSIM_SRV=<サーバ> FCSIM_LIB=<ライブラリ> FCSIM_SER=XYZZY_A FCSIM_ZBX= tests/e2e-sim-faults.sh`（経路が 1 本なら、2 本目の確かめは飛ばす）。

## 2.1 状態を残したまま起こし直す（`-K`・`FCSIM_KEEP_DISK=1`）

既定では、`prepare.sh` を流すたびに差分ディスク（`disk.qcow2`）を作り直すので、VM の中の状態（mhVTL のテープの中身など）は消える。
テープの中身と、外のプログラムが持つ記録（どのテープの何番目のファイルに何があるか）の寿命をそろえたいときは、
`prepare.sh seed -K …` か `FCSIM_KEEP_DISK=1 prepare.sh single|ha` で、前の差分ディスクを使い続ける。

- 差分ディスクを作るとき、置き場に `disk.base`（元の共通イメージの絶対パス・その sha256・作った時刻）を書く。
- 使い続ける前に、`disk.qcow2` の元のパスと `disk.base` の記録が、いまの共通イメージ（`FCSIM_BASE`）のパスと sha256 に合うかを確かめる。
  合わない・記録が無いときは、何も変えずに終了コード 3 で止まる（元の共通イメージが変わったあとに古い差分を使うと、ディスクが壊れるため）。
- **種（`seed.iso`）も前のものをそのまま使う**。cloud-init の instance-id が変わらないので、初回の設定（ファイルの書き込み・ユーザ・
  サービスの登録）はもう走らない。その代わり、`-n`・`-f`・`-L` などの引数を変えても効かない。設定を変えたいときは `-K` を外して作り直す
  （テープの中身は消える）。
- 置き場に差分ディスクが無いときは、ふつうに作る（最初の 1 回）。
- FC の設定（FCoE・LIO のターゲット・mhVTL）は、VM の起動のたびに `fcsim-setup.service` が流すので、使い続けても毎回整う。
- 試験の障害注入（`/_sim/v1`）の状態は mhVTL のメモリにだけあるので、起こし直すと消える。

## 3. コンテナ（qemu-bmc）への渡し方

| 何 | どこへ | 例 |
|---|---|---|
| 種とディスクの置き場 | `/vm` に bind。qemu-bmc は `/vm/disk.qcow2` から起動し、`VM_CDROM: /vm/seed.iso` で種を読む | `- <置き場>:/vm` |
| 共通イメージ | **同じ絶対パス**で bind（読むだけ）。`disk.qcow2` は差分で、元のイメージを絶対パスで指している | `- /path/.cache:/path/.cache:ro` |
| KVM と TAP | `devices: [/dev/net/tun, /dev/kvm]` | |
| FC の NIC | `VM_NETWORKS`（コンテナのインタフェース名）・`VM_MACS`（`-f` と同じ MAC）・`VM_NET_MTU: "9500"` | `VM_NETWORKS: eth1,eth2` |
| 管理の NIC | qemu の user-net を、`-m` と同じ MAC で。外から届くのは `hostfwd` で転送したポートだけ（コンテナの管理アドレスのそのポート） | `QEMU_EXTRA_ARGS: "-nic user,model=virtio-net-pci,mac=52:54:00:aa:00:01,hostfwd=tcp::22-:22"` |

- ライブラリで libmgr を使うなら、`hostfwd=tcp::8080-:8080,hostfwd=udp::161-:161` も足す。
- VM のユーザは `lab`（sudo はパスワード無し、鍵は `-k`）。ラボ専用。
- FC スイッチ（fcsw）は `/etc/fcsw/switch.yaml` に設定を bind する（`lab/external/switch.yaml`）。ポートはコンテナのインタフェース名で書く。
  ゾーンのメンバーは WWPN（`20:00:<MAC>`）。

## 4. データ網（FC の経路）の MTU

FCoE は 1 フレームが最大 2158 バイトある。経路の途中のどこかが 1500 だと、FCF の広告（FIP）が通らずログインできない（試作で確かめた）。

- VM の中の FC の NIC：`fcoe-up.sh`・`lib-setup.sh` が 9000 にする。
- VM の NIC・TAP・コンテナの中のブリッジ：qemu-bmc の `VM_NET_MTU`（このリポジトリでは `"9500"`）。
- コンテナとコンテナの間のリンク（veth）：**containerlab の既定の MTU（9500）に頼っている**。lab/clab の構成も `lab/external/example.clab.yml` も、
  リンクに `mtu` を書いていない。
- 自分のトポロジで、リンクに `mtu` を書く・containerlab の既定を変える・ブリッジや別のネットワークを挟むときは、経路の全部を 2158 以上
  （そろえるなら 9000 以上）にすること。足りないと、スイッチのネームサーバに何も載らず、サーバの `fc_host` が Online にならない。

## 5. 版の固定

| 何 | 固定のしかた | 場所 |
|---|---|---|
| Debian 13 のクラウドイメージ | 日付の版（`20261021-2638`）と SHA512。合わなければ `build-base.sh` が止まる | `lab/images/build-base.sh` |
| mhVTL | commit `469d79dc5b518d9eead9a18e6d3272b89350118e`（タグ 1.8-0_release）と、`lab/images/base/patches/` のパッチ | `lab/images/base/user-data` |
| fcsw・監視局のベースイメージ | `golang:1.26-bookworm`・`debian:bookworm-slim` を digest で | `image/Dockerfile`・`image/mon/Dockerfile` |
| qemu-bmc | タグ `0.11.0` と digest `sha256:3ab6ea901b1ae8a510a2a140113fd1d80ad0f3ad2c26327ab04ef94f95bbdfc9` | 各 `.clab.yml` |

- Debian のクラウドイメージの日付の版は、配布元で古いものが消えることがある。消えたら、新しい版の URL と `SHA512SUMS` の値に書き換える。
- Debian のパッケージ（fcoe-utils・targetcli-fb など）は、build したときの Debian 13 のリポジトリのもの（固定していない）。
