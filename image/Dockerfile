# FC スイッチのシミュレーター fcsw のコンテナイメージ。
#   docker build -f image/Dockerfile -t fc-switch-sim/fcsw:dev .
# 設定は /etc/fcsw/switch.yaml。ポートのインタフェース（ContainerLab のリンク）が現れるまで待ってから始める。
# ベースイメージは digest で固定（再現性のため。上げるときは docker pull して RepoDigests を書き換える）
FROM golang:1.26-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /fcsw ./cmd/fcsw

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 tcpdump curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /fcsw /usr/local/bin/fcsw
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fcsw", "-config", "/etc/fcsw/switch.yaml", "-listen", ":8080"]
