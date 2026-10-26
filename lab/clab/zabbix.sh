#!/bin/bash
# 冗長構成（fctape-ha）の管理網に Zabbix 7.0 を足す・外す。ContainerLab の外の、ふつうの docker のコンテナとして動かす
# （動いている構成に ContainerLab でノードを足すと、VM まで作り直しになるため）。
#   zabbix.sh up     DB（.40）・サーバ（.41）・Web（.42、http://172.30.31.42:8080/、Admin / zabbix）・
#                    SNMP トラップの受信（.43、udp 1162）を起こす。トラップは共有のボリュームのファイルでサーバに渡る
#   zabbix.sh down   消す
set -euo pipefail
NET=fctape-ha-mgmt
C="sudo docker"
# DB のパスワードは ZBX_DB_PASSWORD で変えられる（既定 zabbix。ラボ専用）
DBENV=(-e POSTGRES_USER=zabbix -e POSTGRES_PASSWORD="${ZBX_DB_PASSWORD:-zabbix}" -e POSTGRES_DB=zabbix)
case "${1:-up}" in
  up)
    $C network inspect $NET >/dev/null || { echo "管理網 $NET が無い。先に冗長構成を deploy する" >&2; exit 1; }
    $C rm -f fctape-zbx-db fctape-zbx-server fctape-zbx-web fctape-zbx-traps >/dev/null 2>&1 || true
    $C volume rm fctape-zbx-snmptraps >/dev/null 2>&1 || true
    $C volume create fctape-zbx-snmptraps >/dev/null
    $C run -d --name fctape-zbx-traps --network $NET --ip 172.30.31.43 \
      -v fctape-zbx-snmptraps:/var/lib/zabbix/snmptraps zabbix/zabbix-snmptraps:alpine-7.0-latest >/dev/null
    $C run -d --name fctape-zbx-db --network $NET --ip 172.30.31.40 "${DBENV[@]}" postgres:16-alpine >/dev/null
    $C run -d --name fctape-zbx-server --network $NET --ip 172.30.31.41 "${DBENV[@]}" \
      -e DB_SERVER_HOST=172.30.31.40 -e ZBX_CACHESIZE=64M -e ZBX_ENABLE_SNMP_TRAPS=true \
      -v fctape-zbx-snmptraps:/var/lib/zabbix/snmptraps:ro zabbix/zabbix-server-pgsql:alpine-7.0-latest >/dev/null
    $C run -d --name fctape-zbx-web --network $NET --ip 172.30.31.42 "${DBENV[@]}" \
      -e DB_SERVER_HOST=172.30.31.40 -e ZBX_SERVER_HOST=172.30.31.41 -e PHP_TZ=Asia/Tokyo \
      zabbix/zabbix-web-nginx-pgsql:alpine-7.0-latest >/dev/null
    for i in $(seq 1 60); do
      curl -s -m 3 -X POST -H 'Content-Type: application/json-rpc' http://172.30.31.42:8080/api_jsonrpc.php \
        -d '{"jsonrpc":"2.0","method":"apiinfo.version","params":{},"id":1}' | grep -q result && { echo "Zabbix が起きた"; exit 0; }
      sleep 5
    done
    echo "Zabbix が起きない" >&2; exit 1 ;;
  down)
    $C rm -f fctape-zbx-db fctape-zbx-server fctape-zbx-web fctape-zbx-traps >/dev/null 2>&1 || true
    $C volume rm fctape-zbx-snmptraps >/dev/null 2>&1 || true ;;
esac
