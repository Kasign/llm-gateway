# FREE-01 现有 Provider 接入能力盘点

> 状态：已完成
> 更新日期：2026-11-23
> 仓库：Kasign/llm-gateway
> 分支：dev
> 审查基线：`7e908199c98d312e402b4e0ee9b3924f2e4ce1d0`
> 任务规划：[整体任务规划](../../整体任务规划.md)
> 技术方案：[产品能力技术方案](../../产品能力技术方案.md)

## 1. 任务目标与范围

盘点现有 Provider 接入链路，明确免费模型聚合可以复用的能力、协议覆盖范围和真实限制。只做现状审查，不为完成盘点而新增抽象或重写 Provider。

审查范围：

- Provider 接口、Capabilities、Registry 和本地 Provider 装配；
- 远程 Provider 配置持久化、凭据加密、动态注册/更新/删除；
- OpenAI-compatible 传输、鉴权、模型映射、连接测试和安全边界；
- 模型发现 Catalog、模型登记表与远程 Provider 模型目录的职责区别；
- Provider 限流、Token 预算、Usage 与上游错误处理；
- 现有真实 Provider E2E 测试覆盖和未验证边界。

## 2. 现有调用链

### 2.1 Registry 与推理入口

- `internal/provider/provider.go` 定义稳定的 `Provider` 接口：`ID()`、`Capabilities()`、`Forward()`。
- `Registry` 已支持 `Register`、`RegisterLocal`、`Resolve`、`Replace`、`Unregister`；Resolve 通过工厂获得 Provider 实例，热更新不影响已经取得旧实例的在途请求。
- `ModelDescriptor`、`Capabilities`、`Usage` / `UsageSource` 等公共类型已经存在。
- `internal/server/pipeline_hooks.go` 是组合根：首次构造推理 Pipeline 时创建本地 Registry；`internal/server/pipeline_forward.go` 在唯一出网点按已解析的 Provider ID 调用 Registry，再执行同一 Provider 的 Forward。
- Registry 当前没有 Provider 枚举/模型目录查询接口，也没有把 `ModelDescriptor` 作为注册元数据统一维护的能力。Provider 注册与远程模型目录目前是两套不同的概念。

### 2.2 本地 Provider

`internal/provider/local_http.go` 的 `NewLocalRegistryWithBackendClient` 注册两个内置 Provider：

- `llamacpp`
- `ollama`

二者复用本地 HTTP Provider 适配层，能力声明为 OpenAI-compatible Chat Completions + Streaming；本地模型管理能力通过可选的 `LocalModelManager` 单独注入。远程 Provider 不需要实现空的模型加载/卸载方法。

### 2.3 远程 Provider 管理与凭据

远程 Provider 由 Web 管理台管理：

- 配置以 `remote_providers:web:v1` 为键保存到现有 Settings 存储，不另建 Provider 配置表。
- API Key 在写入前使用 AES-GCM 加密；密钥由独立的 Provider 加密配置提供。
- API 响应只返回是否已配置和掩码，不返回明文 Key 或密文。
- 启动时读取设置、解密 Key、校验配置并注册启用的远程 Provider。
- 新建、编辑、启用、停用、清除 Key 和删除操作会同步持久化与 Registry 注册状态；活动 Provider 的危险变更受到保护。
- 每个远程 Provider 目前固定由 `buildRemoteProvider` 构造 `OpenAICompatibleProvider`，没有可配置的 Adapter 类型或协议选择字段。

因此，现有凭据加密、管理 API、设置持久化、动态 Registry 更新可以直接复用；免费模型目录不应再保存一份 Provider Key。

## 3. 现有远程 Adapter 能力

### 3.1 已有能力

`internal/provider/openai_compatible.go` 已提供：

- HTTPS Base URL 校验；
- 明文 HTTP 与私网访问默认关闭，需管理员显式开启；
- DNS/IP 校验、每次新连接重新解析并直连已验证 IP，避免 DNS rebinding；
- 不使用环境代理、不跟随重定向；
- 请求/响应大小上限；
- Authorization Bearer、`api-key`、`x-api-key` 三种服务端 API Key Header；
- 非流式 JSON 和 SSE 流式转发；
- Provider 级并发上限、可选 FIFO 等待队列和等待超时；
- 请求总时限、RPM 限制及 Gateway 侧每日 Token 预算；
- 上游 HTTP 状态、Retry-After 和限流相关响应头的受控透传；
- 稳定 Provider 错误分类，常见 429 映射为 rate-limit 类错误。

这些能力足以作为首批标准 OpenAI-compatible 免费 Provider 的接入基础，不需要为每家厂商复制 HTTP 客户端、并发器或凭据管理。

### 3.2 协议覆盖与声明缺口

当前 `Capabilities` 只有：

- `protocol`
- `chat_completions`
- `streaming`
- `local_model_management`

`OpenAICompatibleProvider.Capabilities()` 目前只声明 `openai-compatible`、Chat Completions 和 Streaming。

但传输实现还包含协议特化逻辑：

- `/v1/responses`：识别 Responses SSE 序号并按 Responses 错误事件收口；
- `/v1/messages`：识别 Anthropic Messages 请求，处理 `anthropic-*` 请求头、响应限流头、错误信封和流式错误事件；
- 其他合法相对 `/v1/*` 路径由通用转发路径处理。

这说明底层转发已有部分跨协议兼容代码，但能力声明没有明确表达 Responses / Anthropic Messages 支持状态，也没有模型级 Tool Calling、Vision、JSON/Structured Output、Reasoning 等能力声明。不能因为某 Provider 使用 OpenAI-compatible Adapter，就推断其每个模型都支持这些能力。

现有 `internal/provider/anthropic_messages_e2e_test.go` 是显式 opt-in 的真实端点测试；`docs/real-provider-e2e-matrix.md` 当前将 Anthropic Messages、OpenAI-compatible Chat Completions 和 Responses 真实端点状态都记录为“未测”。因此，本盘点只确认代码路径存在，不把它升级为真实厂商兼容性验收通过。

### 3.3 BaseURL 路径前缀的实际兼容边界

补充核对 `internal/provider/openai_compatible.go` 的 `buildUpstreamURL` 后，发现现有 Adapter 对 BaseURL 路径前缀有一个具体限制：

- 当 BaseURL 路径以 `/v1` 结尾时，Adapter 会从请求路径中剥离 `/v1`，再与 BaseURL 前缀拼接；Groq 的 `/openai/v1`、OpenRouter 的 `/api/v1`、Cerebras 的 `/v1`、Cloudflare Workers AI 的 `/ai/v1` 都符合这一规则。
- Google Gemini OpenAI-compatible Base URL 是 `/v1beta/openai/`，并不以 `/v1` 结尾。当前拼接会多追加一段 `/v1`，与官方 `/v1beta/openai/chat/completions` 和 `/v1beta/openai/models` 路径不一致。
- 这不是 Google API 不兼容，而是当前 Adapter 的 BaseURL 路径拼接规则表达能力不足。后续应评估通用的 API 路径前缀/端点映射配置，不为 Google 单独复制 Adapter。

连接测试也有相同的通用限制：当前固定请求 `/v1/models`。Cloudflare Workers AI 的模型目录使用 `GET /accounts/{account_id}/ai/models/search`，不属于 OpenAI `/v1/models`；因此连接测试失败不能直接判定其 Chat Completions 推理端点不可用。

## 4. Provider 管理与模型目录现状

### 4.1 连接测试

`POST /api/admin/providers/{id}/test` 复用现有 Adapter，使用 4 秒 Context 请求上游 `GET /v1/models`，最多返回 100 个模型 ID。

限制：

- 连接测试假设上游提供 OpenAI 风格 `/v1/models`。
- 不支持该模型列表端点的厂商，即使推理端点可用，也可能在管理台连接测试中失败。
- 测试结果只返回模型 ID，不自动形成持久化模型目录，也不自动维护模型映射。
- 因此它是 OpenAI-compatible 模型列表探测，不是通用 Provider 健康检查或全协议兼容测试。

### 4.2 模型选择与对外模型列表

远程 Provider 配置目前保存：

- `default_model`：默认上游模型 ID；
- `model_mappings`：Gateway 对外公开模型别名到上游模型 ID 的静态映射。

普通推理只使用已解析的 Provider 和模型快照。已知公开别名可映射到上游模型；未知客户端模型名不会触发任意上游模型切换，而是沿用 Gateway 默认模型语义。

远程 Provider 的 `/v1/models` 由默认模型和静态映射组装，不是一个持久化、可搜索、带能力元数据的远程模型目录。

### 4.3 现有 Catalog 与 Model 表不能直接替代免费模型目录

- `internal/catalog` 当前只聚合 Hugging Face 与 Ollama Library 的模型发现信息，主要用于本地模型下载/安装前的发现。
- `internal/model.Model` 对应 Gateway 本地模型登记，包含本地路径、文件名、来源、SHA256、加载/使用状态等。
- 两者都不是远程推理 Provider 的免费计划目录，也没有官方免费政策、适用地区、额度、绑卡条件、核验时间和过期治理字段。

后续免费模型目录应独立表达远程模型能力和免费政策，不把远程 API 模型伪装成本地 GGUF 资产，也不复用 HF/Ollama 下载 Catalog 的业务语义。

## 5. 限流、Token 预算与 Usage 边界

- 远程 Provider 已有 Gateway 侧 RPM 配置和 Redis 用量快照。
- 已有 `token_budget_per_day`，按 UTC 日桶在 Gateway 侧预占与结算；预占阶段用请求字节数和最大输出 Token 做保护性估算，发起上游请求后优先按真实 Provider usage 结算。
- 若已发起上游请求但拿不到可靠 usage，预占不会立即释放，以避免可能已经发生的上游计费被错误忽略。
- 这些是 Gateway 管理员配置的本地使用预算，不是供应商账户的免费余额查询，也不代表厂商免费额度的实时剩余值。
- `UsageSource` 已区分 provider / estimated / unknown；免费目录展示必须继续区分官方政策额度、Gateway 实际观测用量和估算值。
- 429 已有稳定 rate-limit 分类；供应商可能使用 402、403、429 或专有错误体表达余额/免费额度耗尽。当前没有统一的厂商免费额度错误码字典或跨厂商额度状态协议。HTTP 非 2xx 状态仍按现有 Adapter 的协议边界返回；后续 FREE-07 应按实际首个 Provider 的官方错误结构验证，不应先设计虚构的通用额度接口。

## 6. 可复用能力与缺口清单

| 能力 | 当前状态 | FREE 阶段处理 |
|---|---|---|
| Provider 推理接口与 Registry | 已有 | 直接复用 |
| 本地 Provider 装配 | 已有 | 不改动 |
| 远程 Provider 管理 API | 已有 | 直接复用 |
| Provider Key AES-GCM 加密 | 已有 | 直接复用，不复制密钥 |
| OpenAI-compatible JSON/SSE Adapter | 已有 | 首选接入路径 |
| SSRF / DNS / Redirect 安全 | 已有 | 直接复用 |
| Provider 并发、队列、RPM | 已有 | 按厂商官方限制配置 |
| Gateway 每日 Token 预算 | 已有 | 只作 Gateway 自身预算，不冒充厂商免费余额 |
| Responses / Anthropic Messages 传输特化 | 部分已有 | 补齐明确能力声明与真实验收前，不标记全兼容 |
| Provider / Model 能力目录 | 不完整 | FREE-03/04 设计独立只读元数据；不扩展热路径 |
| 远程模型目录持久化 | 不完整 | 评估复用模型 ID 探测结果，但必须与免费政策数据分离 |
| 协议感知连接测试 | 不完整 | 只在首个 Provider 需要时增加针对性探测 |
| 厂商免费余额/额度实时读取 | 未有通用能力 | 不假设存在；以官方政策和实际错误验证为主 |
| 免费额度耗尽的跨厂商错误归一化 | 未有 | FREE-07 按真实 Provider 证据处理 |
| 真实 Provider E2E | 当前矩阵未测 | FREE-06/07 对首个 Provider 做受控真实短请求 |

## 7. 结论与后续约束

### 7.1 结论

**现有架构足以启动免费模型聚合，不需要重写 Provider Registry，也不需要为每个免费模型新建 Provider。**

首批优先选择官方 OpenAI-compatible API 的厂商：只要 Base URL、服务端 API Key Header、模型 ID 和流式/错误语义可由现有 Adapter 表达，就复用现有远程 Provider 管理入口。

只有遇到明确的协议/鉴权差异且无法通过现有配置表达时，才增加专用 Adapter。不能因为某厂商有免费模型，就提前为其增加一套 Provider 实现。

### 7.2 FREE-02 进入条件

FREE-02 负责官方免费计划调研，必须逐项记录：

1. 官方免费计划是否仍有效，以及官方来源 URL 和核验日期；
2. 注册、使用地区、账号类型和是否需要绑卡；
3. 免费额度的周期、计量单位、模型范围及 RPM/TPM/并发限制；
4. 官方 API 协议与模型 ID；
5. Chat Completions / Responses / Anthropic Messages、Streaming 等能力的官方声明；
6. 免费额度耗尽时的官方状态码和错误结构（官方未说明则记未知）；
7. 是否允许 Gateway 代理转发及相关条款限制。

候选厂商仍是待核验集合，不在本任务提前确定首个接入对象。完成官方证据比较后，再决定 FREE-03 元数据结构与 FREE-06 首个 Provider。

## 8. 验收记录

- 审查分支：`dev`。
- 审查基线：`7e908199c98d312e402b4e0ee9b3924f2e4ce1d0`。
- 审查方式：对照实际 Go 接口、远程 Provider 管理链路、配置/Settings、模型路由、Catalog、限流与 Token 预算实现。
- 代码变更：无。现有能力足以支撑进入官方计划调研；当前没有证据要求在 FREE-01 中新增代码。
- 真实 Provider 兼容性：未验收。现有 E2E 文档矩阵中的真实端点状态仍为未测，后续只针对最终选定 Provider 做短请求验收。
