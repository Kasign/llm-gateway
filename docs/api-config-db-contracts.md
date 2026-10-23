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
| Web 登录 | `/api/auth/*` | 管理员网页登录、登出、会话和凭据更新。 |

路由注册层只统一包裹 Request ID、调用记录和 Panic Recovery；管理 API 的具体鉴权由各 Handler/认证辅助逻辑执行，审查新的管理路由时需要单独确认认证与 CSRF 边界，不要把“已注册在 `/api/admin` 下”误当成自动获得统一授权。

## 2. 配置契约

| 配置区 | 核心字段/用途 | 生效边界 |
| --- | --- | --- |
| `server` | `host`、`port`、`trusted_proxies` | 代理转发头只信任明确配置的代理来源。 |
| `auth` | Admin API Key、用户名/密码或密码哈希、会话 TTL | 管理员凭据与调用方推理 API Key 是两套身份。 |
| `database` | MariaDB 地址、库/用户、密码、初始化权限、迁移文件 | 启动时连接并执行迁移/结构检查。 |
| `redis` | 地址、密码、DB | 用于 RPM 限流等瞬态状态。 |
| `llama` | 本地后端类型、llama.cpp/Ollama Base URL、上游 Key、超时、模型数、warm-start | 全局 `/v1/*` 初始活动后端配置；管理台切换后活动选择以持久化设置为准。 |
| `providers.openai_compatible[]` | Provider ID、启用状态、Base URL、环境变量 API Key、Key Header、默认模型、HTTP/私网策略、响应体上限 | Provider 静态配置在进程启动时加载；远程 Key 不写入 `gateway_settings` 或管理 API 响应。 |
| `agents[]` | Agent ID、Path Prefix、Provider ID、默认模型 | 注册路由时校验引用；修改后需重启 Gateway，尚无配置热更新。 |
| `storage`、`download` | 模型目录、下载服务地址、超时、User-Agent | 模型文件应放在挂载的持久化目录。 |
| `logging` | 调试日志、请求 payload、SSE 原始事件/预览存储 | `store_payload` 与原始流事件可能保存提示词、代码和模型回答；生产环境必须明确评估隐私与磁盘代价。 |

### 环境变量解析

配置加载先解析 YAML，再只对字符串值中的 `${ENV_NAME}` 做环境变量替换，最后重新序列化 YAML。这样可避免密钥包含引号、冒号或换行时破坏 YAML 结构。缺失的占位符不会被当成空白成功配置：启用的远程 Provider 和其他必需字段仍需通过字段级校验。

建议约束：
- 真实 Provider Key、数据库密码与 Admin API Key 只放在部署环境或受控 Secret 中；不要提交到 `deploy/config.yaml` 或示例文件。
- `allow_insecure_http` 和 `allow_private_network` 是显式的安全例外，只能在确认目标来源可信后启用。
- `providers.openai_compatible` 与 `agents` 当前不由管理台动态创建/修改；这类变更需更新 YAML 并重启。
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
| Provider 固定、Agent 路由和原有 `/v1` 入口并存 | `internal/server/agent_routes_test.go`、配置验证测试和请求级 Provider 选择测试。 | Agent 级独立权限策略、动态重载。 |
| Provider 请求头、API Key 隔离、响应/流错误 | `internal/provider/openai_compatible_test.go` Mock 上游测试。 | 真实 OpenAI/DeepSeek/Anthropic 端到端。 |
| 请求取消与 Finish 顺序 | `internal/server/cancel_request_test.go` 与新增并发竞争测试。 | 在 CI race detector 通过后才算具备竞态检测证据；仍需线上压测基线。 |
| 数据库升级 | `internal/db` 迁移与必需表检查测试。 | 多版本升级账本、真实旧数据的升级演练。 |
| 前端/API 部署一致性 | CI 从前端源码重建 bundle 并与已提交 `web/static/dist/app.js` 做字节比较。 | 真实 1Panel/OpenResty 反向代理部署验收。 |

这份文档是当前契约的索引，不是 OpenAPI 规范；API 若继续演进，应考虑维护机器可读的 OpenAPI 文件，并以自动测试约束路径、请求/响应模型与错误状态，减少 README、前后端与运行代码之间的漂移。
