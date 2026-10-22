# 默认最终阶段 release：使用仓库内已提交的 web/static/dist，不再跑 npm（改 Go 时重建最快）。
# 前端有改动且未本地 make web 时：docker build --target release-with-web .
#
# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS webbuilder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/js ./js
RUN npm run build

FROM golang:1.23-bookworm AS builder
WORKDIR /src
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=sum.golang.google.cn
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# 与上一行 go mod download 共用 /go/pkg/mod 的 BuildKit cache mount；否则 build 阶段看不到已下载模块，会重复拉取。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/llm-gateway ./cmd/gateway

FROM alpine:3.22 AS release-base
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/llm-gateway /app/llm-gateway
COPY migrations /app/migrations
COPY web/index.html /app/web/index.html
COPY web/static/css /app/web/static/css
USER 0:0
EXPOSE 8088
ENTRYPOINT ["/app/llm-gateway"]

FROM release-base AS release-with-web
COPY --from=webbuilder /src/web/static/dist /app/web/static/dist

FROM release-base AS release
COPY web/static/dist /app/web/static/dist
