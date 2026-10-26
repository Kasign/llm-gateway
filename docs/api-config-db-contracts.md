# API、配置、数据库与响应契约盘点

> 审查基线：`dev` 提交 `c18c20f2f47885522cec40cf045d72966d3a1f78`。内容按当前路由注册、配置校验、Provider 转发、响应结束处理和 SQL 迁移代码整理；这是现状清单，不表示所有功能均已完成端到端验收。真实上游验证边界见文末。

## 1. 对外 HTTP API

### 推理与模型发现

| 路径 | 当前行为 | 兼容性与注意事项 |
| --- | --- | --- |
| `GET /health` | 返回活动 Provider、Redis 与本地后端状态。 | 远程 Provider 状态代表“已配置”，健康检查不主动请求供应商；不能把 `ok` 当作远程模型可用性证明。 |
| `/v1/models` | 查询管理台当前活动 Provider 的模型列表。 | 保留现有全局入口，活动 Provider 选择写入持久化设置。 |
| `POST /v1/chat/completions` | 经统一 `proxyV1` 主流程转发。 | 经过调用方 Key 鉴权、RPM 限流、模型 ACL、Pipeline 与审计。 |
| `POST /v1/completions`、`/v1/responses` | 通过统一 `/v1/*` 入口处理。 | Responses 流错误事件有独立的字段/序号语义；流式响应提交后不能再改变 HTTP 状态。 |
| `POST /v1/messages` | Anthropic Messages 兼容透传。 | 有协议专用错误 envelope、32,000,000 字节请求体上限及 Anthropic 头透传；上游非 2xx 且尚未提交响应时按兼容约定保留上游状态/错误正文，详见第 4 节。 |
| `POST /v1/messages/count_tokens` | 经 `/v1/*` 和对应 Agent 前缀透传。 | 是否有效由配置的上游是否支持该路径决定；Gateway 不自行计算 Anthropic token 数。 |
| `/<agent-prefix>/v1/models`、`/<agent-prefix>/v1/*` | 为每个启用的 Agent 固定 Provider 与默认模型，移除前缀后复用同一处理器。 | Agent 与 Provider 由 YAML 在启动时校验并注册；当前没有 Agent 级独立 Key ACL、模型别名映射或热更新。 |

Agent 前缀限制为单个小写路径段（例如 `/deepseek`），禁止与 `/api`、`/v1`、`/static`、`/health` 冲突，Agent ID 和路径前缀分别不能重复。Agent 复用全局推理鉴权、限流、Pipeline 与审计；现阶段不应假定 Agent 前缀自身就是独立的授权边界。

### 管理台与运维路由分组

| 路由分组 | 主要路径 | 功能 |
| --- | --- | --- |
| 管理台资源 | `/`、`/static/*` | 管理页面及静态 bundle。 |
| 模型管理 | `/api/models*`、`/api/hf/*` | 本地模型列表、安装/操作与 Hugging Face 目录查询。 |
| 管理配置 | `/api/admin/bootstrap*`、`/api/admin/backend*`、`/api/admin/inference-params*` | 初始化、后端选择、推理参数。 |
| 本地运行时 | `/api/admin/runtime*`、`/api/admin/ollama*`、`/api/admin/local-inventory` | 本地模型运行时、Ollama 与文件清单管理。 |
| 下载与基准测试 | `/api/downloads*`、`/api/benchmark*` | 下载任务、Benchmark 状态和结果。 |
| 请求审计 | `/api/requests*` | 请求记录筛选与详情。 |
| API Key 管理 | `/api/admin/keys*` | 创建、修改、禁用、轮换、删除、过期时间、RPM、模型 ACL。 |
| 远程 Provider 管理 | `GET/POST /api/admin/providers`、`PUT/DELETE /api/admin/providers/{id}`、`POST /api/admin/providers/{id}/{enable,disable,clear-key,test}` | 部署来源只读；Web 项保存到 `gateway_settings.remote_providers:web:v1`。Key 使用 AES-256-GCM 密文，API 不返回明文或密文；编辑时 Key 留空保留，清除必须调用 `clear-key`。
| Web 登录 | `/api/auth/*` | 管理员网页登录、登出、会话和凭据更新。 |

路由注册层只统一包裹 Request ID、调用记录和 Panic Recovery；管理 API 的具体鉴权由各 Handler/认证辅助逻辑执行，审查新的管理路由时需要单独确认认证与 CSRF 边界，不要把“已注册在 `/api/admin` 下”误当成自动获得统一授权。

## 2. 配置契约

| 配置区 | 核心字段/用途 | 生效边界 |
| --- | --- | --- |
| `server` | `host`、`port`、`trusted_proxies` | 代理转发头只信任明确配置的代理来源。 |
| `auth` | Admin API Key、用户名/密码或密码哈希、会话 TTL | 管理员凭据与调用方推理 API Key 是两套身份。 |
| `database` | MariaDB 地址、库/用户、密码、初始化权限、迁移文件 | 启动时连接并执行迁移/结构检查。 |
| `redis` | 地址、密码、DB | 用于 RPM 限流等瞬态状态。 |
| `llama` | 本地后端类型、llama.cpp/Ollama Base URL、上游 Key、请求时限、模型数、warm-start | `LLAMA_REQUEST_TIMEOUT_SECONDS` 控制本地推理客户端的整体请求时限；显式设为 `0` 时不设客户端整体时限，适合慢速本地模型，调用方取消仍然有效。旧配置未填写时继续默认 3600 秒；正整数上限 604800 秒。全局 `/v1/*` 初始活动后端配置；管理台切换后活动选择以持久化设置为准。 |
| `providers.openai_compatible[]` / `LLM_GATEWAY_PROVIDER_<ID>_*` | Provider ID、启用状态、Base URL、API Key、Key Header、默认模型、公开模型 ID 映射、HTTP/私网策略、响应体上限、并发上限与整体请求时限 | YAML / 环境变量项为部署托管配置，管理台只读显示；环境变量可一次预置多个 Provider，跨来源相同 ID 会在启动校验阶段拒绝。可选 `model_mappings` 将公开 model ID 映射到供应商真实 model ID；`request_timeout_seconds` 为 0 时保留既有 Context 与响应头超时行为，非 0 时限制完整 JSON/SSE 请求时长。 |
| `agents[]` | Agent ID、Path Prefix、Provider ID、默认模型 | 注册路由时校验引用；修改后需重启 Gateway，尚无配置热更新。 |
| `storage`、`download` | 模型目录、下载服务地址、超时、User-Agent | 模型文件应放在挂载的持久化目录。 |
| `logging` | 调试日志、请求 payload、SSE 原始事件/预览存储 | `store_payload` 与原始流事件可能保存提示词、代码和模型回答；生产环境必须明确评估隐私与磁盘代价。 |

### 环境变量解析

配置加载先解析 YAML，再只对字符串值中的 `${ENV_NAME}` 做环境变量替换，最后重新序列化 YAML。这样可避免密钥包含引号、冒号或换行时破坏 YAML 结构。缺失的占位符不会被当成空白成功配置：启用的远程 Provider 和其他必需字段仍需通过字段级校验。

建议约束：
- 真实 Provider Key、数据库密码与 Admin API Key 只放在部署环境或受控 Secret 中；不要提交到 `deploy/config.yaml` 或示例文件。
- `allow_insecure_http` 和 `allow_private_network` 是显式的安全例外，只能在确认目标来源可信后启用。
- `providers.openai_compatible` 与 `LLM_GATEWAY_PROVIDER_<ID>_*` 是只读部署配置；Web 只修改 `source=web` 项。Web 项持久化后动态更新 Registry；同 ID 跨来源冲突拒绝覆盖。Compose 只显式传递示例中的 DeepSeek/OpenAI 字段；使用其他 Provider ID 时，需要在 Compose 的 environment 中显式透传对应字段，避免把含数据库管理员口令的整个 `.env` 暴露给 Gateway 容器。
- `/v1` 全局入口的活动 Provider 可由管理台选择；Agent 入口不读取这个活动指针，使用自己的固定 Provider/默认模型。

## 3. 数据库与迁移契约

当前 `internal/db.Migrate` 读取配置指定的迁移 SQL（默认 `/app/migrations/001_init.sql`），按分号拆分并执行语句，再调用 `ensureColumns` 增补历史字段，最后校验必需表存在。

| 表 | 当前职责（按代码访问与结构定义） | 兼容性提醒 |
| --- | --- | --- |
| `api_keys` | 调用方 API Key、状态、过期/限流及访问策略。 | 不应存储或返回明文调用密钥。 |
| `models` | 模型清单及运行时元数据。 | `runtime_json` 由增量结构升级补齐。 |
| `request_logs` | 请求状态、耗时、HTTP 状态、Provider 分析字段。 | `request_url`、`upstream_http_status`、`ttft_ms` 等是兼容升级重点。 |
| `request_payloads` | 可选请求/响应正文及请求/上游 Header。 | 正文可能包含敏感业务内容；由日志存储设置控制。 |
| `request_stream_events` | SSE 事件序号、时间、类型、字段和可选原始行。 | 原始流事件字段通过 `ensureColumns` 升级。 |
| `gateway_settings` | 持久化全局活动后端及管理台可调设置。 | 不适合存储远程 Provider 密钥，也不承载 Agent 路由静态配置。 |

**迁移系统现状与风险**：目前没有独立的 `schema_migrations` 版本账本；SQL 执行依靠单个 `001_init.sql` 加程序内 `ensureColumns`，重复列通过匹配 MariaDB 的 duplicate-column 错误忽略。当前 SQL 是简单 DDL，可按现有契约运行，但继续增加多版本/数据迁移时应先引入有序、可追踪、可观测的迁移记录，而不是无限扩展硬编码列清单。分号拆分也不适合包含分号的过程体/复杂 SQL。

## 4. 关键响应语义

1. **网关状态与上游状态分开记录。** 如果状态行尚未提交，网关可返回上游错误状态；如果流式 warm-start 或正文已写出，HTTP 状态不可逆，日志必须保留 Gateway HTTP status 与 upstream HTTP status 两个字段，不能伪装为未发生错误。
2. **流式提交后不能自动换上游或重试。** 已发送的文本和事件无法撤回。只有在响应尚未提交时，未来受控重试/故障转移才有讨论空间。
3. **协议错误格式不同。** OpenAI Chat/Responses 使用各自错误结构；Anthropic Messages 要求 `type:error` 与 `error.type/message` 信封。不能对所有协议用一个 JSON 错误对象。
4. **Anthropic Messages 有意保留部分原始上游错误。** 当上游已返回非 2xx 且 Gateway 尚未提交响应时，该路径保留上游状态、原始错误正文与必要重试/使用量响应头，以便兼容客户端进行能力降级。其他协议仍使用脱敏错误正文。此例外要求 Provider 指向管理员信任的 Anthropic-compatible 上游；不应将不可信回显服务配置到该路由。
5. **错误状态类型按 HTTP 状态区分。** 例如 402 计费、403 权限、413 请求过大、429 限流、504 超时、529 过载；连接失败等不存在上游响应正文的情况由 Gateway 生成安全错误信封。
6. **请求大小上限不同。** 常规推理入口 64 MiB；Anthropic Messages 与其子路径 32,000,000 字节。Provider 侧也复核 Messages 上限，避免绕过 Gateway Handler 直接调用适配器时无限制转发。

## 5. 当前验证矩阵与待办

| 验收项 | 当前证据 | 未覆盖部分 |
| --- | --- | --- |
| Provider 固定、Agent 路由和原有 `/v1` 入口并存 | `internal/server/agent_routes_test.go` 覆盖配置验证、400 个并发模型解析、双 Provider JSON/SSE data-plane（#101）、模型列表 helper ACL（#104）及完整 Handler 的缺失/无效 Key 401、模型列表 ACL 和 Redis RPM 429（#108）。HTTP 测试使用 test SQL driver；RPM 走真实 Redis。 | Agent 级独立权限策略、动态重载、MariaDB 集成及真实供应商 E2E。 |
| Provider 请求头、API Key 隔离、响应/流错误 | Mock 上游、DNS NXDOMAIN/timeout、连接拒绝、ResponseHeaderTimeout 错误映射均由 Actions #92/#96 验证；Agent data-plane 对各自上游密钥隔离由 #99 验证；三项性能基准及 Artifact 由 #94/#99 验证。 | 真实 OpenAI/DeepSeek/Anthropic 端到端、真实 DNS 出口异常与跨公网故障注入；持续性能趋势/线上 SLO。 |
| 请求取消与 Finish 顺序 | `internal/server/cancel_request_test.go` 与新增并发竞争测试。 | 在 CI race detector 通过后才算具备竞态检测证据；仍需线上压测基线。 |
| 数据库升级 | `internal/db` 迁移与必需表检查测试。 | 多版本升级账本、真实旧数据的升级演练。 |
| 前端/API 部署一致性 | CI 从前端源码重建 bundle 并与已提交 `web/static/dist/app.js` 做字节比较。 | 真实 1Panel/OpenResty 反向代理部署验收。 |

这份文档是当前契约的索引，不是 OpenAPI 规范；API 若继续演进，应考虑维护机器可读的 OpenAPI 文件，并以自动测试约束路径、请求/响应模型与错误状态，减少 README、前后端与运行代码之间的漂移。


## CI 性能基线首轮样本（2026-10-23）

Actions #94（提交 `1339f5fb61537c08292aa44cec990c5d930401c9`）在 linux/amd64、Go 1.23.12、AMD EPYC 7763 Runner 上产出：
- Provider loopback Mock 转发：90,352 ns/op、15,795 B/op、146 allocs/op。
- Agent Provider/模型解析：342.0 ns/op、0 B/op、0 allocs/op。
- 请求分析 DTO 生成：1,372 ns/op、592 B/op、17 allocs/op。

这些是首轮可重复比较样本，不是生产请求延迟；Provider Mock 不包含公网网络和真实模型推理。CI Artifact 按 commit SHA 保存 30 天：[性能基线文件](https://github.com/Kasign/llm-gateway/actions/runs/39898164924/artifacts/12477692434)。


### Agent Streaming Provider 契约（CI #101）

Actions #101（提交 `4e7e134a7a19fbe3dc1b2efb05f4383c0bcff978`）通过双 Agent 的非流式 JSON 与流式 SSE 集成测试。测试验证：
- `/deepseek/v1/chat/completions` 与 `/openai/v1/chat/completions` 各自转发到对应 Mock Provider，出站路径归一为 `/v1/chat/completions`。
- Agent 默认模型覆盖客户端提交的不同 model；出站 stream 布尔值与入站一致。
- SSE 返回对应 Provider 的内容和 `[DONE]`；不串用另一 Agent 的模型或 Provider API Key。
- Provider 服务端凭据会转发，Gateway 调用方 `Authorization` 和 `X-API-Key` 不会作为上游凭据泄漏。


### Agent /v1/models ACL（CI #104）

Actions #104（提交 `65d8709ff2fdad3a1c9e59c2b9d300a173f4cac5`）通过模型列表与 ACL 单元测试：不受限 Key 可看到固定目标模型；AllowedModels 可按公开 model ID 或上游 model ID 放行；不匹配时返回空 data 列表。响应 owned_by 取 Agent Provider ID，display_name 为对应上游模型名称，不会列出其他 Provider 的模型。测试覆盖 helper 契约，不声称完整外层 auth/Redis/audit HTTP 集成已经由该用例覆盖。


### Anthropic Messages 成功路径回归（CI #106 已通过）

Provider 契约测试新增：
- Anthropic Messages SSE 成功响应完整透传及 message 生命周期事件覆盖，校验 Provider 凭据、Anthropic-Version、Request-Id、model 覆盖、query 透传。
- Anthropic /v1/messages/count_tokens 成功请求/响应透传，覆盖实际转发路径、服务端模型和凭据边界。
Actions #106（提交 `4e5d69766e9a16a668e2398b918def97cb64d3f2`）全量 CI 已通过；测试仍使用本机 Mock HTTP 服务，真实 Anthropic 供应商 E2E 仍待执行。


### Agent /v1/models HTTP 鉴权与限流（CI #108）

Actions #108（提交 `82bd2f5712167707cbfa528e2d9d31a10172cdd0`）通过 `TestAgentModelsHTTPHandlerEnforcesAuthAndModelACL` 与 `TestAgentModelsHTTPHandlerEnforcesRedisRPMLimit`：
- 实际 `Server.Handler()` 的 Agent 路由：缺失或未知 API Key 返回 401。
- 不受限 Key 只看到该 Agent 的固定模型；AllowedModels 不允许该模型时返回空列表。
- CI 环境连接真实 Redis 限流器；RPM=1 时第一个请求 200，第二个请求 429。
- SQL 部分使用 test-only `database/sql/driver` 返回 API Key 行并接受审计写入，不代表 MariaDB/迁移 E2E；真实 schema 与 SQL 兼容仍需专门集成环境覆盖。


### 可选真实 Provider 验收

`internal/provider/openai_compatible_e2e_test.go` 提供显式 opt-in 的真实 Chat Completions 连通/鉴权检查。必须通过本地环境变量开启，CI 默认跳过，密钥不写入仓库。它覆盖真实 HTTPS 端点上的 Provider 请求与响应格式，但不能替代不同供应商型号、账单、限流策略或 Anthropic 原生协议的单独验收。


### Responses API 成功路径（CI #114 已通过）

`internal/provider/openai_compatible_test.go` 新增 Mock HTTP 测试，验证 `/v1/responses` JSON/SSE 成功路径、query 与 `OpenAI-Beta` 头、服务端 model 覆盖、Provider key 与调用方凭据隔离、Request-Id 响应头和原始 SSE 字节透明传输。Actions #114（提交 `d8bcafee44ccbb7a4a5bd459bf363563ccbb1e1a`）全量 CI 已通过。该测试是 Provider adapter 契约测试，并非真实 OpenAI/Codex 服务端到端。


### Anthropic Messages 真实端点验收

`internal/provider/anthropic_messages_e2e_test.go` 提供默认跳过的真实端点探测；只有显式开启环境变量后才使用本机提供的 API Key 调用上游。CI #116（提交 `594ef12048e0a9dee52bb6c1ae4d780f1cbfaa3a`）通过完整流水线，验证测试可编译且默认跳过；没有真实 API Key，因此真实 Anthropic 供应商端点仍未验收。测试覆盖 HTTPS 配置、`x-api-key`、Anthropic-Version、HTTP 200 和 Messages 响应封套。


### 可选 Responses API 真实端点探测

`TestOpenAIResponsesRealEndpointE2E` 独立 opt-in，复用 OpenAI-compatible E2E 的环境变量并使用 `LLM_GATEWAY_RESPONSES_E2E=1` 开启。用例通过真实 HTTPS Provider 发起一次短 `/v1/responses` JSON 请求，检查 `object=response`、ID、completed 状态与非空 output。它默认不运行、不消耗真实模型调用；CI 成功仅表示测试可编译和默认跳过，不代表真实 Codex/OpenAI 端点已验收。


### Web 多 Provider 存储与运行时契约（2026-10-26）

- Provider 可配置 `request_timeout_seconds`（0–86400）；非 0 时设置独立 HTTP Client 的整体期限，覆盖请求发送、等待响应和读取完整响应体，因此也会限制 SSE 生成的最长时长。0 表示不增加 Provider 级整体期限，仍受请求 Context、拨号/TLS 和响应头超时约束。Web 编辑、YAML、`.env` 与 Compose 透传同一字段。

- 新增/编辑/启用/停用/删除前校验 Provider 配置与安全策略；先保存到 `gateway_settings.remote_providers:web:v1`，再更新 Registry。Registry 替换只改变新请求解析到的 Provider 工厂；已解析的请求与 SSE 保留旧实例，不被主动取消。
- Web Provider API Key 使用 `LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY`（base64 编码的 32 字节随机值）进行 AES-256-GCM 加密；数据库只保存版本化密文。列表/详情 API 只返回 `api_key_configured` 和通用掩码，不返回 `api_key` 或 `api_key_ciphertext`。编辑时空 Key 保留旧值，显式清除走独立 `clear-key` 动作。
- YAML 与 `.env` 预置项只读。部署 ID（含禁用项）仍占用该 ID；Web 创建/编辑遇到冲突拒绝覆盖。停用/删除当前活动 Provider 返回 409；新增 Provider 不改变活动上游。
- `model_mappings` 为公开 ID -> 上游真实 ID。`/v1/models` 仅返回活动 Provider 的可用别名并执行现有模型 ACL；推理转发使用映射后的上游 ID。未配置映射时，旧 YAML 的默认模型行为不变。
- 连接测试复用生产 Provider 构造器和现有 SSRF/HTTPS/禁止重定向策略，4 秒超时、最多读取 1 MiB，只返回模型 ID 与脱敏错误。
- 连接测试允许检查已停用的 Web Provider，但 ID 来源冲突或密文不可解密的记录不会被测试路由接管；部署项仍通过 `?source=deployment` 显式指定。