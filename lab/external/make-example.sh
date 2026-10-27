#!/bin/bash
# 外部のトポロジの例（example.clab.yml）の下ごしらえ：prepare.sh seed で種とディスクを作り、.clab.yml を埋める。
#   lab/external/make-example.sh        → lab/external/run/（種とディスク）・lab/external/run.clab.yml
#   sudo containerlab deploy -t lab/external/run.clab.yml
# スイッチのイメージ fc-switch-sim/fcsw:dev は先に作っておく（docker build -f image/Dockerfile -t fc-switch-sim/fcsw:dev .）。
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
BASE=$(realpath -m "${FCSIM_BASE:-$ROOT/.cache/fcsim-base.qcow2}")
SEEDS=$PWD/run
P=../clab/prepare.sh
"$P" seed -d "$SEEDS/tapehost" -n tapehost -r server -f 52:54:00:fc:0a:01
"$P" seed -d "$SEEDS/tapelib" -n tapelib -r library -i 1 -f 52:54:00:fc:0a:02 -I 0x2000525400fc0a01 -L ""
sed -e "s|__SEEDS__|$SEEDS|g" -e "s|__BASE_DIR__|$(dirname "$BASE")|g" example.clab.yml > run.clab.yml
echo "用意できた。sudo containerlab deploy -t lab/external/run.clab.yml"
