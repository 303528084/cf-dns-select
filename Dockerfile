# ---- Go 构建阶段 ----
# 前端不在此处构建：web/static 需预先产出（本地 npm run build，或由 CI 原生构建后传入上下文），
# 避免多架构构建时用 QEMU 模拟执行 npm（极慢且易卡死）。
FROM golang:1.23-alpine AS builder

ARG VERSION=dev
# Go 模块代理：默认使用 goproxy.cn（国内可达）。
# 如你的构建环境可直连官方源，可在构建时覆盖：
#   docker build --build-arg GOPROXY=https://proxy.golang.org,direct .
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /cf-dns-select .

# ---- 运行阶段 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 1000 dsa && adduser -S -u 1000 -G dsa dsa

WORKDIR /app
COPY --from=builder /cf-dns-select /usr/local/bin/cf-dns-select
COPY ip.txt ipv6.txt /app/

# 数据目录：config.json 持久化在此
RUN mkdir /app/data && chown -R dsa:dsa /app
USER dsa

ENV DSA_CONFIG=/app/data/config.json
EXPOSE 8080

HEALTHCHECK --interval=60s --timeout=5s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8080/api/health >/dev/null 2>&1 || exit 1

ENTRYPOINT ["cf-dns-select"]
CMD []
