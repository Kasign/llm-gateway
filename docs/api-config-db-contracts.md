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
| 请求审计 | `/api/requests*` | 请求记录筛选与详情。`GET /api/requests` 支持 `provider` 查询参数，对 Provider ID 做不区分大小写的完整 ID 精确匹配；`GET /api/requests/stats` 返回受限时间窗口内的聚合调用、延迟及 Token 来源汇总。 |
| API Key 管理 | `/api/admin/keys*` | 创建、修改、禁用、轮换、删除、过期时间、RPM、模型 ACL。 |
| 远程 Provider 管理 | `GET/POST /api/admin/providers`、`PUT/DELETE /api/admin/providers/{id}`、`POST /api/admin/providers/{id}/{enable,disable,clear-key,test}` | 部署来源只读；Web 项保存到 `gateway_settings.remote_providers:web:v1`。Key 使用 AES-256-GCM 密文，API 不返回明文或密文；编辑时 Key 留空保留，清除必须调用 `clear-key`。列表同时返回 RPM 用量、当前 Gateway 进程的并发占用快照，以及该进程自启动以来因并发上限拒绝的新请求累计数；计数通过原有刷新请求更新，不使用额外后台轮询，多副本环境下不代表全局并发总数，进程重启后拒绝累计数归零。
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
| `providers.openai_compatible[]` / `LLM_GATEWAY_PROVIDER_<ID>_*` | Provider ID、启用状态、Base URL、API Key、Key Header、默认模型、公开模型 ID 映射、HTTP/私网策略、响应体上限、并发上限、Provider 级 RPM 与整体请求时限 | YAML / 环境变量项为部署托管配置，管理台只读显示；环境变量可一次预置多个 Provider，跨来源相同 ID 会在启动校验阶段拒绝。可选 `model_mappings` 将公开 model ID 映射到供应商真实 model ID；`requests_per_minute=0` 时不启用 Provider 级 RPM，正数时按 UTC 固定分钟窗口对该 Provider 的所有调用方/Agent 共享计数，超限返回 429，配置 RPM 后 Redis 不可用则 fail-closed 返回 503；该预算只拒绝新请求，不中断在途流式生成。`request_timeout_seconds` 为 0 时保留既有 Context 与响应头超时行为，非 0 时限制完整 JSON/SSE 请求时长。 |
| `agents[]` | Agent ID、Path Prefix、Provider ID、默认模型 | 注册路由时校验引用；修改后需重启 Gateway，尚无配置热更新。 |
| `storage`、`download` | 模型目录、下载服务地址、超时、User-Agent | 模型文件应放在挂载的持久化目录。 |
| `logging` | 调试日志、请求 payload、SSE 原始事件/预览存储、请求历史保留策略 | `store_payload` 与原始流事件可能保存提示词、代码和模型回答；`retention_days=0` 时永不自动删除历史数据，正数则按保留天数分批清理已结束请求。批次默认 500、每轮最多 10 批，默认每小时运行一次；仍在运行的请求不清理。 |

### 环境变量解析

配置加载先解析 YAML，再只对字符串值中的 `${ENV_NAME}` 做环境变量替换，最后重新序列化 YAML。这样可避免密钥包含引号、冒号或换行时破坏 YAML 结构。缺失的占位符不会被当成空白成功配置：启用的远程 Provider 和其他必需字段仍需通过字段级校验。

建议约束：
- 真实 Provider Key、数据库密码与 Admin API Key 只放在部署环境或受控 Secret 中；不要提交到 `deploy/config.yaml` 或示例文件。
- `allow_insecure_http` 和 `allow_private_network` 是显式的安全例外，只能在确认目标来源可信后启用。
- `providers.openai_compatible` 与 `LLM_GATEWAY_PROVIDER_<ID>_*` 是只读部署配置；Web 只修改 `source=web` 项。Web 项持久化后动态更新 Registry；同 ID 跨来源冲突拒绝覆盖。Compose 从主 `deploy/.env` 显式传递 DeepSeek/OpenAI 示例变量；其他 Provider ID 可通过可选 `deploy/providers.env` 注入。该专用文件只应包含 `LLM_GATEWAY_PROVIDER_<ID>_*`，避免向 Gateway 容器注入主 `.env` 中的数据库管理员密码等无关凭据。旧部署缺少此文件时 Compose 不会因此失败。
- `/v1` 全局入口的活动 Provider 可由管理台选择；Agent 入口不读取这个活动指针，使用自己的固定 Provider/默认模型。

## 3. 数据库与迁移契约

当前 `internal/db.Migrate` 读取配置指定的迁移 SQL（默认 `/app/migrations/001_init.sql`），按分号拆分并执行语句，再调用 `ensureColumns` 增补历史字段，最后校验必需表存在。

| 表 | 当前职责（按代码访问与结构定义） | 兼容性提醒 |
| --- | --- | --- |
| `api_keys` | 调用方 API Key、状态、过期/限流及访问策略。 | 不应存储或返回明文调用密钥。 |
| `models` | 模型清单及运行时元数据。 | `runtime_json` 由增量结构升级补齐。 |
| `request_logs` | 请求状态、耗时、HTTP 状态、Provider 分析字段。 | 新增可空 `provider_id`，由解析出的实际 Provider 写入；`idx_provider_started` 支持按 Provider 和时间窗口聚合。列表筛选通过规范化后的 `provider_id=?` 精确查询，旧记录保持 NULL/空值，不根据当前活动上游反推历史 Provider。 |
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

### 请求历史保留配置

`logging.retention_days` 默认为 `0`，表示完全禁用自动清理，兼容旧部署并保护历史记录。设为正数后，以请求开始时间计算保留期限，只删除已结束请求；在途状态 `running` 不删除。每批默认处理 500 条，每轮最多 10 批，每小时运行一次；每批单独事务删除 `request_stream_events`、`request_payloads` 和 `request_logs`。允许的配置范围为保留天数 0–36500、批次 1–5000、周期 60–86400 秒。清理工作由后台任务执行，日志会记录失败和实际删除数量；自动清理不承诺能替代数据库备份。


### Provider RPM 实时用量

`GET /api/admin/providers` 在原有安全视图外附加当前 UTC 分钟窗口的用量字段：`requests_per_minute_used`、`requests_per_minute_remaining`、`requests_per_minute_reset_seconds`、`requests_per_minute_usage_available`。用量读取只对 Redis 做批量 `MGET`，不递增计数、不查询 MariaDB；Redis 查询失败时 Provider 列表依旧返回配置数据，只将用量标记为不可用。计数可能包含超额后被拒绝的新请求尝试，因此在突发限流时已用计数可能大于限额，剩余额度会归零。


### Provider RPM 超限响应

Provider 级 RPM 超限返回 HTTP 429，错误码为 `provider_rate_limit_exceeded`，并设置 `Retry-After` 响应头（秒），提示客户端当前 UTC 固定分钟窗口何时重置。该值按响应生成时的剩余窗口秒数计算；客户端应把它作为最早重试时间参考，并继续遵守自己的退避策略。Redis 不可用导致的 503 不附加此头。

### 调用记录 Provider 筛选契约

- 请求：`GET /api/requests?limit=100&provider=deepseek`。它与现有 `model`、`status`、`key` 条件组合使用，不改变返回体结构；不传 `provider` 或传空白值时不增加 Provider 条件。
- 匹配：Provider ID 会先去除首尾空白并转成小写，再按完整 ID 相等匹配；`deep` 不会命中 `deepseek`。该参数不是模型名，也不是模糊搜索。
- 数据层：`Repo.Query` 的旧签名保留用于详情及现有调用方；`QueryWithProvider` 承载可选 Provider 条件，SQL 直接比较规范化的 `provider_id`，以便继续利用 Provider 索引。
- 实时数据：列表缓存键包含 Provider ID；在途请求先与 LiveRegistry 快照合并，再按最新 Provider ID 过滤，避免模型解析完成后仍留在旧 Provider 的筛选结果中。

### 调用统计接口

- `GET /api/requests/stats?range=24h&provider=deepseek` 仅管理员可访问；`range` 允许 `24h`、`7d`、`30d`，默认 `24h`。不支持任意长的时间窗口，避免无界历史聚合。
- 统计窗口为 `[from,to)`，按请求的 `started_at` 归属；Provider ID 可选，先去空格并转小写后完整匹配。统计不会继承列表的模型、状态或 Key 名筛选条件。
- `requests.total_requests` 包含进行中请求；成功率分母是已结束请求（总数减 running），包括已取消请求。延迟平均值与区间分布只纳入已结束且有 `latency_ms` 的记录。分布区间为 <1 秒、1–3 秒、3–10 秒、10–30 秒、30–60 秒和 ≥60 秒。
- Token 数据按 `analysis_json.throughput.source` 分类：`usage/upstream_usage` 为上游 usage；`llama_timings` 为本地引擎计数；其它或缺失来源归类到 `unknown_source`。旧记录不根据 Token 数值推断来源。


## 6. 机器可读 OpenAPI 契约（首版范围）

### operationId 元数据门禁（2026-10-26）

OpenAPI 当前包含 63 个字面路径模板和 72 个 HTTP operation。Agent 前缀映射展开后，路由门禁核对 67 个运行时路由路径。CI 检查 operationId 不重复，且每个 operation 都有 summary 和 responses；除 `GET /health`、`POST /api/auth/login` 外，每个 operation 必须显式声明 security。健康检查和登录是有意公开的操作，不应通过伪造认证要求来消除检查告警。

请求日志保留状态接口已添加到该契约范围，并由完整 Actions #225 验证。该检查与路径双向对账共同构成路由/operation 的静态门禁；它不保证每个 JSON Schema 的字段、可空性和 Go Handler 序列化完全相同。字段契约仍以 Handler、实际响应结构和功能 Review 为依据。


机器可读规范位于 [`docs/openapi.yaml`](openapi.yaml)，采用 OpenAPI 3.1.0。首版覆盖健康检查、通用推理协议入口、配置的 Agent 前缀推理入口、请求统计，以及 Web 管理 Provider 的 CRUD 与操作端点。

**这是分阶段建立的契约，不是完整 API 清单。** 模型下载、运行时管理、Benchmark、API Key 管理、推理参数、Ollama 管理及其他历史管理端点仍以本文件和各自部署文档为准；在逐项核实 Handler、认证和响应结构前，不应在 OpenAPI 文件中猜测字段。当前也尚未建立 OpenAPI 与 Handler 的自动差异检查。

推理端点使用调用方 Gateway API Key（`Authorization: Bearer` 或 `X-API-Key`）；管理端点要求有效的管理员网页登录态或部署配置的管理员凭据。不要把调用方 API Key、远程 Provider Key 与管理员凭据混为一谈。Provider 管理 API 的返回对象只包含 Key 是否已配置及掩码，不返回明文或密文。

本地慢模型还受多个独立超时层控制：`llama.request_timeout_seconds: 0` 关闭 Gateway 本地 HTTP Client 的整体请求期限；`warm_start` 在请求等待首 Token 时发送 SSE 保活；OpenResty 的 `proxy_read_timeout` / `proxy_send_timeout` 与 Codex、Cursor 等客户端期限需独立配置。关闭 Gateway 整体时限不会屏蔽客户端断连或管理端取消。


## 7. API Key 与管理会话契约

`docs/openapi.yaml` 已增加调用方 API Key CRUD、启用/禁用/轮换，以及控制台登录、会话查询、注销与凭据更新端点。

- 调用方 Gateway Key 用于访问 `/v1/*`；它与管理员会话 Token、`X-Admin-Key` 和远程 Provider 的供应商 Key 不同。管理员请求支持 Bearer 管理会话或 `X-Admin-Key`；修改控制台用户名/密码只允许 Web 登录会话。
- `POST /api/admin/keys` 和 `POST /api/admin/keys/{id}/rotate` 会返回一次明文调用方 Key。数据库仅保存摘要，列表和元数据响应不含明文或哈希；轮换后旧 Key 立即失效。
- 管理会话由 `POST /api/auth/login` 建立，Token 在后续请求通过 `Authorization: Bearer` 发送，不是浏览器 Cookie。登录按客户端 IP 进行限流；Web 会话的服务端有效性还依赖 Redis 会话记录及登录凭据版本。
- `PUT /api/auth/credentials` 需要当前密码；新用户名或密码留空表示保留当前值。修改后会更新凭据版本并更新当前会话版本，其他旧版本会话不再有效。
- 规范对以上接口记录了实际响应结构与状态码，但管理端点的错误详情仍共用宽泛的安全错误 Schema。下一步需逐个梳理模型、下载、运行时、Benchmark 和推理参数管理 API，再考虑自动契约差异检查。


## 8. 请求审计、运行时与 Benchmark 契约

机器可读契约新增 `/api/requests` 列表/清理、`/api/requests/{id}` 详情、`/api/admin/audit/metrics`、`/api/models`、活动运行时概览/预览/选择/卸载，以及异步 `/api/benchmark` 创建与查询。

- 请求列表支持 `limit`、`request_id`、`model`、`status`、`key`、`provider` 筛选；删除请求会清空请求历史和实时看板记录，是破坏性操作。
- 请求详情可能返回保存的 prompt、请求/响应正文、请求头、响应头及 SSE 事件。是否完整留存受 `logging.store_payload` 与审计记录是否成功等条件影响，管理接口不应暴露给非可信使用者。
- 审计队列指标是内存快照，不查询数据库；运行时模型管理接口只对本地 llama.cpp/Ollama 执行 load/unload。远程 Provider 的模型生命周期由供应商管理。
- Benchmark 创建任务后立即返回 job ID 和 poll URL；任务状态表在进程内，重启后不承诺恢复。模型生成较慢时，客户端应轮询状态接口，不应把异步 Benchmark 当同步 HTTP 推理请求。
- 本批响应中动态变化的运行时与详情对象采用宽松 Schema，并未伪造所有字段的静态保证；剩余下载、HuggingFace 搜索、Ollama 专属操作、配置引导与推理参数路由仍待逐项覆盖。


## 9. Benchmark 的可选整体时限

`POST /api/benchmark` 的后台任务现在遵循 `llama.request_timeout_seconds` 的零值语义：正数限制整个后台任务时长，0 不追加整体 Deadline。Benchmark 的 Stop/DELETE 取消信号通过独立父 Context 保留，不会因关闭超时而失效。该设置只控制 Gateway 侧总时限，不会延长调用方客户端自己的超时，也不代表上游永远可用。

### 请求日志保留状态 API

`GET /api/admin/retention/status` 提供受管理员鉴权保护的只读快照，包括是否启用、保留期限、批次/间隔配置、最近执行开始/结束时间、执行耗时、删除行数、进程累计删除量和失败次数。正常情况下此接口不查询数据库，返回 `Cache-Control: no-store`。

清理失败只向 API 暴露稳定的 `cleanup_failed` 代码和失败时间，真实数据库诊断仍写入 Gateway 服务日志。该指标保存在当前进程内，Gateway 重启后计数清零；`last_run_status` 表示最近一次运行状态，`last_failure_*` 保留最近一次失败历史，供外部监控器轮询。此接口不主动推送告警。
