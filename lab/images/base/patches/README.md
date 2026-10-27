# mhVTL へのパッチ

共通の VM イメージ（`lab/images/build-base.sh`）で、mhVTL 1.8-0_release（469d79d）に当てるパッチ。
mhVTL は GPL-2.0 で配られていて、ここのパッチもそれに従う（mhVTL を直すものなので、GPL-2.0 の下にある）。

| ファイル | 由来 | 中身 |
|---|---|---|
| `0001-mhvtl-lto10-support.patch` | upstream の [markh794/mhvtl](https://github.com/markh794/mhvtl) のコミット `1cd21f737dde4d48b99f5eccfb38bdaeaa2d9711`（作者 Matej Kenda）を、1.8-0_release の上にそのまま cherry-pick したもの | LTO-10（Ultrium 10・10 Premium）の密度・媒体の種類と、ドライブ ULT3580-TDA |
| `0002-drives-1-2-use-lto10.patch` | このリポジトリ | 既定の構成のライブラリ 10 のドライブ 1・2 を LTO-10 にし、初期のテープのバーコードを合わせる |
| `0003-changer-sim-fault-injection.patch` | このリポジトリ | 試験用の障害注入：チェンジャー（vtllibrary）に、MOVE MEDIUM・INITIALIZE ELEMENT STATUS を指定の sense で失敗させる障害、扉の開閉（開いている間はロボットの動作に NOT READY 04h/03h）、扉を閉じたあとの UNIT ATTENTION 28h/00h を足す。vtlcmd の `fault`・`door` で操作する |

## 0003 の命令（vtlcmd）

ライブラリの番号（既定の構成では 10）に送る。どれも `OK`（`fault status` は状態の1行）を返すまで待つ。

| 命令 | 意味 |
|---|---|
| `fault move <key> <asc> <ascq> [any\|load\|unload]` | MOVE MEDIUM（と INITIALIZE ELEMENT STATUS 0x07・0xE7）を、この sense（16 進）で失敗させる。`load` はドライブへ入れる移動だけ、`unload` はドライブから出す移動だけ（INITIALIZE は向きによらず失敗）。消すまで続く |
| `fault clear` | 障害を消す |
| `fault status` | `fault=… key=… asc=… ascq=… direction=… door=open\|closed door_notready=… ua_pending=… becoming_ready=<残り秒>` |
| `door open [notready]` | 扉を開ける。ロボットを動かす命令（MOVE MEDIUM・EXCHANGE MEDIUM・INITIALIZE ELEMENT STATUS）は NOT READY 04h/03h。`notready` を付けると TEST UNIT READY と READ ELEMENT STATUS も 04h/03h |
| `door close [noclear] [becoming <秒>]` | 扉を閉じる。次の命令（どの経路からでも、全体で1回）に UNIT ATTENTION 28h/00h を返す。既定で障害も消す（`noclear` で残す）。`becoming` を付けると、その秒数の間は NOT READY 04h/01h を返し、そのあと 28h/00h |

- 判定の順：オフライン（04h/12h）→ 電源投入の UA（29h/00h、元からある）→ 04h/01h → 28h/00h → 扉の 04h/03h → 障害の sense。INQUIRY・REPORT LUNS・REQUEST SENSE は元のとおり対象外。
- 見回り用のビット：CDB の CONTROL バイトの bit 6・7（ベンダー固有）を立てた命令は、04h/01h・28h/00h・扉の `notready` を受けず、28h/00h を消費しない（管理 API の見回り sim/libmgr が使う）。ビットは命令を処理する前に消す（mhVTL の READ ELEMENT STATUS は byte 11 が 0 でないと ILLEGAL REQUEST にするため）。MOVE MEDIUM・EXCHANGE MEDIUM はこのバイトを別の意味に使うので対象外。オフラインと 29h/00h は元のとおり返す。
- 状態は vtllibrary のメモリにだけあり、デーモンを起こし直すと消える。

ライセンスの本文は mhVTL の COPYING（GPL-2.0）を見ること。このリポジトリのほかの部分のライセンスは別。
