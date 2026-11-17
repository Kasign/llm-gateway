# LLM Gateway

个人使用的模型接入、管理与能力扩展平台。通过统一 Gateway 管理本地模型与远程 Provider，提供 API 凭据隔离、请求治理、调用审计、Token 用量和 Web 管理台。

项目不以让慢速本地模型承载 Codex、Claude Code 等 Coding Agent 长请求为主要目标，也不是通用 Chat UI 或多用户 SaaS。

## 当前能力

- **推理接口**：统一入口支持 OpenAI-compatible Chat Completions、Completions、Responses，以及 Anthropic Messages 等已适配协议；具体能力以 Provider 兼容矩阵和真实验收记录为准。
- **活动模型管理**：通过管理台选择活动 Provider/模型。普通推理请求体中的 `model` 不用于切换活动模型，避免请求触发本地模型加载/卸载。
- **凭据与访问控制**：调用方 API Key、Web 登录 Session、管理员 API Key、上游 Provider Key 分离；支持调用方权限、限流和密钥轮换。
- **本地模型管理**：支持 llama.cpp / Ollama 相关的模型目录、下载和运行时管理能力。
- **远程 Provider**：通过 Web 管理台配置，凭据加密保存，不在 API 响应中回显。
- **请求审计与 Token 用量**：记录请求状态、延迟、TTFT、Token 来源和错误；Token 用量统计不等同于费用核算。
- **Web 管理台**：管理 Provider、模型、调用方 Key、请求记录、运行状态和 Benchmark。

## 产品能力路线

接下来按以下顺序规划扩展：

1. **工具能力**：建立受控 Tool Registry / Runtime，先接入已部署的 SearXNG。
2. **本地模型分析**：拆解请求阶段耗时，结合 llama.cpp timings 和资源快照定位推理瓶颈。
3. **免费模型聚合**：维护官方免费模型与 API 额度信息，复用现有 Provider 接入能力。

以上三项目前属于规划任务，不代表已实现。唯一任务来源、架构方案和验收标准见 [产品能力技术方案](docs/产品能力技术方案.md)，任务状态以[整体任务规划](docs/整体任务规划.md)为准。

## 文档入口

- [文档索引与维护规范](docs/文档索引.md)
- [整体任务规划](docs/整体任务规划.md)
- [当前任务](docs/当前任务.md)
- [技术架构](docs/architecture.md)
- [API、配置、数据库与响应契约](docs/api-config-db-contracts.md)
- [配置说明](配置说明.md)
- [1Panel 部署](README-1PANEL.md)
- [控制台密码保护](控制台与密码说明.md)
- [未关闭问题与后续验收](docs/known-issues-and-followups.md)
- [生产部署验收清单](docs/production-acceptance-checklist.md)

## 运行边界

- 本项目不负责模型推理本身；推理由已配置的本地或远程 Provider 完成。
- 默认 Compose 只部署 Gateway，不重建现有 llama.cpp、Ollama、Redis 或 MariaDB 服务。
- 默认只将 Gateway 端口绑定到宿主机回环地址，由 1Panel / OpenResty 按实际部署配置提供 HTTPS 入口。
- 不要将 llama.cpp、Redis、MariaDB 或管理接口直接暴露到公网。

## 快速启动

在项目根目录执行：

```bash
cp .env.example .env
cp config.example.yaml config.yaml
```

按 [配置说明](配置说明.md) 填写 Docker 网络、模型目录、MariaDB/Redis 连接、Gateway 管理凭据和 Provider Key 加密密钥，再按 [1Panel 部署说明](README-1PANEL.md) 启动：

```bash
sudo docker compose up -d --build
```

远程 Provider 在 Web 管理台配置，不通过 YAML 或环境变量预置。首次保存 Provider Key 前，必须配置并备份 `LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY`。
