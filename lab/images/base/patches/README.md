# mhVTL へのパッチ

共通の VM イメージ（`lab/images/build-base.sh`）で、mhVTL 1.8-0_release（469d79d）に当てるパッチ。
mhVTL は GPL-2.0 で配られていて、ここのパッチもそれに従う（mhVTL を直すものなので、GPL-2.0 の下にある）。

| ファイル | 由来 | 中身 |
|---|---|---|
| `0001-mhvtl-lto10-support.patch` | upstream の [markh794/mhvtl](https://github.com/markh794/mhvtl) のコミット `1cd21f737dde4d48b99f5eccfb38bdaeaa2d9711`（作者 Matej Kenda）を、1.8-0_release の上にそのまま cherry-pick したもの | LTO-10（Ultrium 10・10 Premium）の密度・媒体の種類と、ドライブ ULT3580-TDA |
| `0002-drives-1-2-use-lto10.patch` | このリポジトリ | 既定の構成のライブラリ 10 のドライブ 1・2 を LTO-10 にし、初期のテープのバーコードを合わせる |

ライセンスの本文は mhVTL の COPYING（GPL-2.0）を見ること。このリポジトリのほかの部分のライセンスは別。
