.PHONY: web build docker docker-web
web:
	cd web && npm ci && npm run build

build:
	go build -trimpath -ldflags="-s -w" -o bin/llm-gateway ./cmd/gateway

# 日常 Go 开发：复用仓库已有前端 dist，不执行 Node/npm 构建。
docker:
	DOCKER_BUILDKIT=1 docker build --target release-with-bundled-web -t llm-gateway .

# 完整发布构建：使用 BuildKit 缓存 Go/npm 依赖，并重新构建前端资源。
docker-web:
	DOCKER_BUILDKIT=1 docker build --target release -t llm-gateway .
