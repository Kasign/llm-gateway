# release 使用 webbuilder 构建最新前端；release-with-bundled-web 仅复用仓库现有 dist，供日常 Go 开发快速构建。
#
# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS webbuilder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/js ./js
RUN npm run build

FROM golang:1.27.4-bookworm AS builder
WORKDIR /src
ARG GOPROXY=https://goproxy.cn,direct
ARG GATEWAY_BUILD_REVISION=
ARG GATEWAY_BUILD_MODIFIED=
ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=sum.golang.google.cn
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
# 仅复制 Go 编译输入，文档、前端和部署配置变化不会触发 Go 重新编译。
# 与 go mod download 共用 /go/pkg/mod 的 BuildKit cache mount。
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X=github.com/whl/llm-gateway/internal/buildinfo.Revision=${GATEWAY_BUILD_REVISION} -X=github.com/whl/llm-gateway/internal/buildinfo.Modified=${GATEWAY_BUILD_MODIFIED}" -o /out/llm-gateway ./cmd/gateway

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

FROM release-base AS release-with-bundled-web
COPY web/static/dist /app/web/static/dist

FROM release-base AS release-with-web
COPY --from=webbuilder /src/web/static/dist /app/web/static/dist

# 默认发布目标始终使用本次构建生成的前端资源。
FROM release-with-web AS release
