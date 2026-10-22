#!/bin/bash
cd "$(dirname "$0")/../.."
for n in srv lib; do [ -f lab/run/$n.pid ] && kill "$(cat lab/run/$n.pid)" 2>/dev/null; done
sudo pkill -f 'bin/fcsw' 2>/dev/null
sleep 1
for t in tap-srv tap-lib; do sudo ip link del $t 2>/dev/null; done
true
