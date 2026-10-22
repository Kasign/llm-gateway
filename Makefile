.PHONY: web build docker docker-web
web:
	cd web && npm install && npm run build

build:
	go build -trimpath -ldflags="-s -w" -o bin/llm-gateway ./cmd/gateway

# 日常改 Go：用仓库内 dist，不跑 webbuilder（快）
docker:
	docker build -t llm-gateway .

# 仅在容器内重打前端时使用（会 npm ci，首次或 lock 变更较慢）
docker-web:
	DOCKER_BUILDKIT=1 docker build --target release-with-web -t llm-gateway .
