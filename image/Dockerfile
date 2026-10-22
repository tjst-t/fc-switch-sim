# FC スイッチのシミュレーター fcsw のコンテナイメージ。
#   docker build -f image/Dockerfile -t fc-switch-sim/fcsw:dev .
# 設定は /etc/fcsw/switch.yaml。ポートのインタフェース（ContainerLab のリンク）が現れるまで待ってから始める。
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /fcsw ./cmd/fcsw

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends iproute2 tcpdump curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /fcsw /usr/local/bin/fcsw
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fcsw", "-config", "/etc/fcsw/switch.yaml", "-listen", ":8080"]
