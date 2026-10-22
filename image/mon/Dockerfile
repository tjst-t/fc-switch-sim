# 監視局：SNMP のトラップを受けて標準出力に 1 行ずつ出す（docker logs で読む）。MIB も入れておく。
#   docker build -f image/mon/Dockerfile -t fc-switch-sim/mon:dev .
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends snmptrapd snmp \
    && rm -rf /var/lib/apt/lists/*
COPY docs/FCSIM-TAPE-LIBRARY-MIB.txt docs/FCSIM-FC-SWITCH-MIB.txt /usr/share/snmp/mibs/
EXPOSE 162/udp
ENTRYPOINT ["snmptrapd", "-f", "-Lo", "-On", "--disableAuthorization=yes", "-F", "TRAP %B %V| %v\n", "udp:162"]
