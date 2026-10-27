# LLM Gateway V11

面向个人远程 Coding Agent 的本地 LLM Gateway。设计目标：Mac 上的 Cursor / Codex / Claude Code 只访问 Gateway；Gateway 再调用已有的 llama.cpp。公网只开放 OpenResty，不直接暴露 llama.cpp、Redis、MariaDB。

## V12 功能

- OpenAI-compatible：`/v1/models`、`/v1/chat/completions`、`/v1/completions`、`/v1/responses`、其他 `/v1/*` POST 路径透传（包括 Anthropic-compatible `/v1/messages`）。
- API Key：创建、编辑、启用/禁用、Rotate、删除、过期时间、RPM、模型 ACL；支持 `Authorization: Bearer` 与 `X-API-Key`。
- 每次 `/v1/*` 调用创建调用记录，包含 API Key、Client、模型、HTTP 状态、Latency、TTFT、Token、错误等；可选保存 payload。
- Hugging Face GGUF：列出仓库中的 GGUF、选择量化、下载、断点续传、SHA-256、取消、重试、重启恢复。
- llama.cpp：健康检查、刷新、Load、Unload、删除；按 `models_max` 串行控制模型加载。
- Web：Dashboard、模型下载/管理、API Key、请求查询/详情、Benchmark、系统状态。
- 管理台全局状态（单容器 AdminState、bootstrap/lite）：见 **[docs/admin-state.md](docs/admin-state.md)**。
- API、Provider/Agent 配置、数据库迁移与关键响应契约：见 **[docs/api-config-db-contracts.md](docs/api-config-db-contracts.md)**。
- 配置来源、优先级、生效时机与环境变量注入清单：见 **[docs/configuration-matrix.md](docs/configuration-matrix.md)**。
- 机器可读 API 契约（覆盖推理入口、调用方 API Key、登录会话、请求统计和 Web Provider 管理，仍为分阶段覆盖）：见 **[docs/openapi.yaml](docs/openapi.yaml)**。
- Redis：RPM 限流、下载进度、瞬态状态。
- MariaDB：模型、API Key、请求日志、可选 payload 持久化。

## 运行边界

本版本不负责主机 Suspend、Wake-on-LAN 等电源管理功能。llama.cpp、Redis、MariaDB 使用现有容器，不由本 Compose 重建。

## 控制台登录与改密码

Web 登录仅使用密码，可在账号设置里启用/关闭密码保护及修改密码；部署与重置说明见 **[控制台与密码说明.md](控制台与密码说明.md)**。

## 1Panel 部署

地址、登录、思考开关分别写在哪一层，见 **[配置说明.md](配置说明.md)**。部署步骤见 **[README-1PANEL.md](README-1PANEL.md)**。

在项目根目录执行：

```bash
cp .env.example .env
cp config.example.yaml config.yaml
cp providers.env.example providers.env
```

填写现有 Docker network、模型目录、MariaDB root 密码、Gateway 数据库密码和 Admin API Key，再：

```bash
sudo docker compose up -d --build
```

本 Compose 只创建 `llm-gateway`。默认只绑定 `127.0.0.1:8088`，由 1Panel/OpenResty 负责公网 HTTPS 反代。
