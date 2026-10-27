# 監視局：SNMP のトラップを受けて標準出力に 1 行ずつ出す（docker logs で読む）。MIB も入れておく。
#   docker build -f image/mon/Dockerfile -t fc-switch-sim/mon:dev .
# ベースイメージは digest で固定（image/Dockerfile と同じもの）
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update && apt-get install -y --no-install-recommends snmptrapd snmp \
    && rm -rf /var/lib/apt/lists/*
COPY docs/FCSIM-TAPE-LIBRARY-MIB.txt docs/FCSIM-FC-SWITCH-MIB.txt /usr/share/snmp/mibs/
EXPOSE 162/udp
ENTRYPOINT ["snmptrapd", "-f", "-Lo", "-On", "--disableAuthorization=yes", "-F", "TRAP %B %V| %v\n", "udp:162"]
