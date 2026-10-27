# LLM Gateway 技术指导与整改路线

> 最近一次完整 CI 验收：提交 `82bd2f5712167707cbfa528e2d9d31a10172cdd0`，GitHub Actions #108（运行 `39904722392`，success）。该运行通过 Agent `/v1/models` 外层鉴权/ACL/Redis RPM HTTP 集成、race detector、性能基准与 Artifact、`go vet`、Gateway 构建及前端 bundle 检查。

> CI 执行 `go test ./...`、`go vet ./...`、Gateway 构建、管理台源码构建、bundle 语法与关键字段检查，并已增加源码构建产物与提交 bundle 的精确字节比较。工作流不应自动向 `dev` 推送构建产物；提交管理台源码改动时，应本地重新生成并提交 `web/static/dist/app.js`。真实供应商端到端、负载/竞态压测和线上网络行为不在 CI 覆盖范围内，不能仅凭 CI 通过宣称这些场景已验收。

## 1. 目标与边界

本项目的长期定位应是**统一模型接入与请求治理网关**，而不是只给本地 llama.cpp 添加一个 HTTP 代理层。

目标：

1. 对 Cursor、Codex、Claude Code 和自有应用提供稳定、受控的网关入口。
2. 统一管理调用方 API Key、权限、限流、请求取消、超时和审计。
3. 同时接入本地模型（llama.cpp、Ollama）和远程模型服务（例如 OpenAI、DeepSeek，以及后续支持的其他厂商）。
4. 在不改变模型实际回答的前提下，提供可信的响应状态、延迟、TTFT、Token 用量与错误记录。
5. 支持本地优先、远程按需选择的路由策略，但不让日志、统计或管理台轮询成为推理主链路的瓶颈。
6. 保持凭据隔离、内容最小化留存和可观测性；默认不将提示词、代码和模型回答作为普通运行日志永久保存。

非目标：

- Gateway 不负责实现模型推理本身。
- 不应假定每个 Provider 都支持本地模型加载、卸载、磁盘文件管理、相同 API 协议或相同能力。
- 不应承诺对任意请求路径、任意工具类型和任意厂商实现百分之百透明兼容。兼容范围应由协议适配器和测试矩阵明确说明。

## 2. 当前架构与主流程

### 2.1 现有组成

| 模块 | 当前职责 | 设计判断 |
|---|---|---|
| `cmd/gateway` | 配置、数据库迁移、启动 HTTP 服务与退出 | 作为组合根保留 |
| `internal/core` | 固定推理 Pipeline，不依赖数据库或 net/http | 当前最值得保留的边界 |
| `internal/server` | HTTP 路由、鉴权、模型解析、运行时管理、日志观察 | 职责仍偏多，后续逐步拆分 |
| `internal/llama` | llama.cpp / Ollama HTTP 客户端和模型运行时操作 | 应演进为本地 Provider 适配器，不应成为所有厂商的万能客户端 |
| `internal/model` | 当前模型登记与持久化 | 未来需要把“模型身份”和“本地 GGUF 文件”区分开 |
| `internal/auth` | 调用方 API Key 管理与校验 | 保留；与厂商凭据严格分离 |
| `internal/ratelimit` | Redis 限流与瞬态状态 | 保留并加强原子性和失败策略 |
| `internal/audit` + `internal/store` | 异步审计队列与 MariaDB 持久化 | 思路正确，但热路径仍有同步日志处理 |
| `internal/live` | 当前请求的内存快照，供管理台实时查看 | 应设定明确内存预算和数据留存规则 |
| `web/` | 管理台与可视化 | 不应通过高频完整轮询干扰推理服务 |

### 2.2 当前请求流程

现有 HTTP 推理入口大致如下：

```text
客户端
  -> HTTP 路由 /v1/*
  -> 方法检查、Gateway API Key 鉴权、RPM 限流
  -> 读取请求体、创建超时/取消 Context
  -> core.Pipeline.Run
       Parse -> Resolve -> Stamp -> Mutate -> Guard
       -> WarmStart -> Forward -> Finish
  -> 向客户端返回响应
```

`internal/core.Pipeline` 的固定主流程是合理方向。鉴权和限流放在入口层，Provider 请求发送收敛到 Forwarder，日志通过 Observer 接入，可以减少核心流程对存储、管理后台的耦合。

但目前 `Resolve` 后的模型就绪检查与请求级 `allowModelsGuard` 的执行顺序需要调整：当前 Pipeline 先跑全局 Guard，再跑本次请求附加 Guard；自动加载模型可能发生在模型 ACL 检查之前。权限检查应先于加载、计费或任何其他有副作用的动作。

当前 HTTP 推理会忽略请求体里的 `model`，使用管理台选定的活动模型；这应视为现有产品语义，而不是 OpenAI API 的通用行为。文档、`/v1/models` 和实际路由必须保持一致。

## 3. 日志审查：会不会影响速度和准确性？

结论：**MariaDB 写入是异步的，但整个日志路径并非完全异步；在当前实现中，流式日志处理可以直接延迟响应块的 Flush，也会污染部分延迟统计。** 在低流量下，影响可能较小；在长回答、高事件数、打开正文留存或并发请求增加时，影响会放大。目前没有基准测试数据，不应凭代码推断具体会慢多少毫秒或多少百分比。

### 3.1 已有的正确设计

- `audit.Writer` 使用有界队列和后台 Worker，数据库 I/O 通常不会直接阻塞转发 goroutine。
- `core.Pipeline.emit` 会隔离 Observer panic，日志回调异常不会直接改变 Pipeline 的返回结果。
- 正文留存有开关，流式累计正文有单请求缓冲上限。
- 流式事件有数量上限；审计写入失败也会记录日志。
- 网关调用方 API Key 不应透传给上游；当前转发器剔除了相关鉴权头，再单独设置上游密钥。

这些机制应保留，但不足以证明日志对响应完全无影响。

### 3.2 审查发现与当前状态

本节区分已修复的实现缺陷、仍需实测的风险与长期优化项。基线 CI 通过不等于已完成压力测试或真实供应商端到端验收。

#### A. 流式日志处理仍需性能评估

文件：`internal/llama/client.go`、`internal/server/pipeline_forward.go`、`internal/server/pipeline_hooks.go`。

当前状态：流式代理已将上游内容写给客户端并 Flush，再调用观察回调；但 SSE 解析、Token 统计、事件构造与实时看板更新仍有同步工作。数据库异步化本身不能证明观测逻辑对每块 Flush 间隔和整体延迟没有影响。

后续应采用固定模型、固定提示词、固定并发数，比较日志配置开关前后的 TTFT、每块 Flush 间隔、P95/P99、吞吐和 RSS，并把计时采样与可丢弃的诊断事件尽量解耦。在取得可重复数据前，不将该项标记为性能验收完成。

#### B. 正文与流事件留存边界已加固，仍需补数据保留策略

文件：`internal/server/request_trace.go`、`internal/server/pipeline_hooks.go`、配置加载模块。

当前状态：完整请求/响应 Payload、原始 SSE 与内容预览已由独立配置控制；流事件限制为每请求最多 2000 条、单条原始行最多 65535 字节，原始数据与预览共享每请求 512 KiB 的累计留存预算。默认关闭正文、原始 SSE 与预览后，不应把原文或 reasoning 作为普通审计内容保存。

仍需明确数据库保留期限、清理机制，以及高并发下 live 内存快照的全局预算。上述限制降低了单请求留存峰值，但不能代替整体内存压测。

#### C. 请求体与上游响应大小限制已接入

文件：`internal/server/pipeline_http.go`、`internal/llama/client.go`、`internal/provider/openai_compatible.go`。

当前状态：推理请求体有明确的 64 MiB 上限，超过时返回 413；本地上游非流式响应通过有界读取限制大小，远程 Provider 的非流式响应和流式错误处理也有响应体上限。超限时不应把被截断的 JSON 伪装成成功响应。

仍需通过大响应和高并发测试测量内存峰值，并确认部署代理层的 body/idle timeout 限制与 Gateway 的行为一致。

#### D. 审计队列的关闭竞态与关键事件优先级已治理

文件：`internal/audit/writer.go`。

当前状态：入队、关键容量预留和 Shutdown 通过同一生命周期锁协调；关键请求创建/最终状态优先于普通诊断任务；队列满和关闭后的丢弃有计数与日志。该实现避免向已经关闭的 channel 发送，但在数据库长时间不可用或队列持续满载时，普通任务仍可能被丢弃。

后续需将队列长度、排队时间、写入失败和丢弃量接入可采集指标，并以数据库故障、满载和退出期间仍有在途请求的场景进行压力测试。

#### E. Gateway/上游状态分离与流式错误收口已接入

文件：`internal/server/response_sink.go`、`internal/server/pipeline_forward.go`、`internal/server/finish_status_test.go`、`internal/provider/openai_compatible.go`。

当前状态：请求记录分别保存 Gateway 已提交状态与上游 HTTP 状态。远程 Provider 流式调用遇到 401、429、503 等非 2xx 时：
- 如果 Gateway 尚未提交响应，返回原上游 HTTP 状态和脱敏 JSON 错误体，不把供应商原始错误正文或凭据上下文传给客户端。
- 如果状态行已经提交（例如 warm-start 已发送 SSE 注释、客户端因此收到 200），HTTP 状态无法再改；适配器会追加安全的 SSE `event: error`，并记录真实上游错误状态。
- Chat Completions 继续使用 `data: {"error": ...}` 形状；`/v1/responses` 使用顶层 `type/code/message/param/sequence_number` 错误事件。序号观察只读取已转发的 SSE 数据，不改写有效负载；观察器的行/事件缓存有硬上限。协议依据：[Responses API 流式事件](https://developers.openai.com/api/reference/resources/responses/streaming-events)。
- 传输中断也用稳定的流式错误事件收口；不会自动重试已经开始输出的流，避免重复生成内容。

现有单元测试覆盖主要分支，但真实供应商、Codex/SDK 的实际解析和代理链路仍需端到端验证。

### 3.3 推荐的日志分层

| 级别 | 内容 | 默认策略 | 对推理路径的要求 |
|---|---|---|---|
| L0：请求结果 | request_id、模型/Provider、HTTP 状态、错误码、耗时、Token 来源 | 始终记录 | 热路径只写轻量状态；异步持久化 |
| L1：性能事件 | 首字节、TTFT、阶段耗时、重试/限流/取消事件 | 始终记录关键字段；详细事件有上限 | 轻量时间戳；禁止在 Flush 前做大块解析 |
| L2：协议诊断 | SSE 事件类型、finish_reason、usage 原始结构摘要 | 可配置、可采样 | 不记录完整内容，必须有总字节预算 |
| L3：请求/响应正文 | Prompt、代码、模型答案、完整 SSE | 默认关闭，按请求临时开启 | 与常规审计分离、加密、限额、到期删除 |

日志应遵循“**先记录发生了什么，再异步补充诊断细节**”。任何统计和观察逻辑都不能改写上游响应、重排响应内容或决定请求成败。

## 4. 扩展外部大模型服务的目标架构

### 4.1 核心原则：抽象推理能力，不强迫所有后端共享模型管理能力

当前 `internal/llama.Client` 同时承载推理转发和本地模型管理，后端类型仅包括 llama.cpp 和 Ollama。GPT、DeepSeek 等远程服务通常不需要 Gateway 加载/卸载本地文件，也不一定支持相同协议或完整的模型运行时查询。

建议逐步引入以下逻辑模块，迁移可以分阶段完成，不要求一次性重写项目：

```text
HTTP/API 层
  -> 认证与调用策略
  -> 请求规范化 + 模型路由
  -> Provider Registry
       -> Local llama.cpp Adapter
       -> Local Ollama Adapter
       -> OpenAI-Compatible Adapter
            -> OpenAI
            -> DeepSeek
            -> 其他兼容厂商
       -> 后续：Anthropic / 其他协议 Adapter
  -> 统一响应观察器与结果日志
  -> 客户端
```

外部厂商接入初期，优先选择提供 OpenAI-compatible Chat Completions 接口的供应商，通过配置不同的 Base URL、模型 ID、鉴权方式和能力声明完成接入。不能据此假设 Responses、Anthropic Messages、工具调用、视觉或 reasoning 字段都可以直接通用，必须逐项验证。

### 4.2 建议的接口边界

建议将“推理”和“本地运行时操作”拆成两类接口：

- **Inference Provider**：发送请求、支持流式和非流式响应、返回上游状态/头/usage/错误、支持取消和超时。
- **Local Model Manager（可选能力）**：本地模型扫描、加载、卸载、下载、内存快照。
- **Capabilities**：声明支持的协议、Chat Completions、Responses、工具调用、流式、视觉等能力。
- **Model Router**：根据逻辑模型 ID、Provider、调用方 ACL 和策略决定实际路由。
- **Credential Store**：安全获取上游 API Key。调用方 Gateway Key 与厂商 API Key 必须是两个完全不同的凭据域。

外部 Provider 不应为了满足本地管理接口而实现空操作的 Load/Unload；应通过能力声明决定管理台显示哪些操作。

### 4.3 模型与 Provider 配置

当前 `settings.Upstream` 表示单个活动后端，配置中的类型被限制为 `llamacpp` / `ollama`。若将来只是不断往这里增加 GPT、DeepSeek 等值，会把单活动后端模型逐渐变成难维护的厂商分支。

建议在数据库逐步建立以下概念（具体表结构应在编码前定稿）：

- `providers`：Provider 类型、显示名、协议、基础 URL、启用状态、超时与能力配置。
- `provider_credentials`：凭据引用、密文、轮换与状态信息；不要把明文密钥放进普通 Provider JSON、日志或管理 API 响应。
- `models`：稳定的 Gateway Model ID、Provider ID、远程模型 ID、显示名、能力、启用状态。
- `routing_policies`：默认路由、调用方允许使用的模型、Provider 优先级及明确的回退策略。

本地 GGUF 路径、量化格式、下载状态属于本地模型扩展信息，不应成为远程模型记录必填字段。

### 4.4 Provider 接入安全

- 厂商凭据只在网关到上游的服务端请求中注入；永远不返回给客户端，也不出现在 URL、错误正文、日志或实时请求头详情中。
- 外部 Provider 的 URL 只能由受信任管理员配置；限制 scheme 和目标地址，防止 SSRF、内网探测以及误把密钥发送给不可信主机。
- 公网厂商通常应要求 HTTPS，并限制重定向行为，避免 Authorization 头泄漏到非预期域名；自定义 Transport 必须保留 TLS 校验和 HTTP/2 能力，并对 DNS、TLS 握手、响应头等待及连接池设置边界。
- `max_response_bytes` 默认 32 MiB，最大 512 MiB；绝对上限用于避免异常配置造成整数溢出或无界内存读取。BaseURL 可包含自定义代理前缀，例如 `/openai/v1`，适配器必须避免重复拼接 `/v1`。
- 对每个 Provider 配置独立超时、并发上限、RPM/Token 限制和错误分类；厂商 429、超时、余额不足、模型不存在等错误应映射为稳定的 Gateway 错误码。
- 自动重试只能在确认没有向客户端提交任何响应内容时进行。SSE 已开始后不能随意换厂商重试，否则会造成重复输出、错误的 Token/费用统计或破坏工具调用。
- 计费与成本统计应基于 Provider 返回的 usage 和定价版本；没有可信 usage 时明确标注未知或估算，不可伪造精度。

### 4.5 活动 Provider 的健康检查语义

健康检查应跟随当前活动后端，而不是无条件探测某个历史本地客户端。切换到远程 Provider 时，网关可能继续保留 `llama.Client` 供本地模型管理 API 使用；这个对象并不代表当前推理路由。否则本地旧服务停机可能导致远程推理实际可用时 `/health` 却报降级，或本地服务正常而远程 Provider 未注册时错误报告正常。

对于本地活动后端，检查对应的本地客户端；对于远程活动后端，启动依赖检查应验证配置和 Provider Registry 已就绪，并明确写出“网络连通性在实际推理时验证”，不能将已配置等同于可达。健康响应应保留既有 `status`、`llama`、`redis` 字段，并额外暴露活动后端与 Provider 状态，以便监控系统区分本地服务健康和远程配置状态。

### 4.6 请求级 Provider 身份固定

一次推理必须在模型解析阶段捕获活动 Provider 身份，之后的请求体模板改写、采样参数合并、模型自动就绪检查和实际转发都沿用该身份。不得在 pipeline 后续阶段重新读取可变的“当前活动 Provider”，否则管理员在请求进行期间切换后端时，可能把旧 Provider 的模型 ID 交给新 Provider。

本地 Provider 也必须按自身 ID 解析并缓存独立的 HTTP 客户端。仅保存 Provider ID 还不够：如果本地 Registry 工厂每次解析时仍读取当前的 `llamaClient()`，切换 llama.cpp/Ollama 后仍会拿错客户端。客户端按后端 ID 和 BaseURL 缓存；地址变化时只替换对应缓存，管理台原有活动客户端入口保持兼容。

内部 benchmark 等进程内请求可以通过专用字段固定 Provider；旧内部调用未提供该字段时继续沿用既有行为。请求触发的自动加载不能把迟到的旧模型加载结果写回当前路由配置；管理员切换与模型自动采纳需要在配置提交阶段互斥，但不能在实际网络加载期间持锁，否则加载成功回调可能死锁。

### 4.7 请求取消与最终状态一致性

管理端取消、客户端断连、网关超时与上游完成可能并发发生。请求状态需要一个明确的线性化点：在 Finish 开始后应拒绝新的管理端取消；若取消先被接受，即使上游恰好同时返回成功，也要把本次调用记为 cancelled。此顺序同时由请求状态和 run registry 保存，防止 callState 清理期间迟到取消绕过 Finish 状态。

审计落库也要遵守同一终态规则。管理端取消可以直接把数据库中的 `running` 行更新为 `cancelled`，而推理 Finish 通过异步审计队列写入；`FinishRequest` 必须保留已经写入的 cancelled 状态、HTTP 状态与取消原因，不能让迟到的成功记录覆盖掉取消结果。内存里没有活动请求时，取消前先冲刷审计队列，以区别“Finish 尚未落库”和“进程重启后留下的 running 行”。指标字段仍可补充，以保留响应字节和 Token 等诊断信息。

### 4.8 配置环境变量展开与启动失败关闭

配置文件中的环境变量应在 YAML 解析为节点树后，只替换字符串值节点，再由 YAML 编码器安全序列化；不要在原始 YAML 文本中直接插入密钥字符串。环境变量可能包含引号、反斜杠、井号或换行，原始文本替换会导致启动解析失败，甚至把值解释成额外配置。

必需配置中的 `${VAR}` 若未展开，必须在启动阶段失败关闭，并仅报告字段路径，不把实际值或密钥写入错误信息。例如管理员 API Key、数据库密码、Redis 配置和活动上游地址不能把未解析占位符当成字面凭据使用。可选登录字段可按环境变量覆盖和默认逻辑处理；未启用 Provider 或非活动本地后端可以保持未配置，但不能因此放宽活动/必需字段的校验。

新增回归测试应覆盖：PBKDF2 哈希中普通 `# LLM Gateway 技术指导与整改路线

> 最近一次完整 CI 成功运行：提交 `1c17e542677e2f448ec44d274d02f7849541b07d`，Actions 运行 `39886198853`（Go CI，success）。该运行通过 Go 单测、`go vet`、Gateway 构建、管理台源码构建，以及源码构建产物与部署 bundle 的精确比较。

> CI 执行 `go test ./...`、`go vet ./...`、Gateway 构建、管理台源码构建、bundle 语法与关键字段检查，并已增加源码构建产物与提交 bundle 的精确字节比较。工作流不应自动向 `dev` 推送构建产物；提交管理台源码改动时，应本地重新生成并提交 `web/static/dist/app.js`。真实供应商端到端、负载/竞态压测和线上网络行为不在 CI 覆盖范围内，不能仅凭 CI 通过宣称这些场景已验收。

## 1. 目标与边界

本项目的长期定位应是**统一模型接入与请求治理网关**，而不是只给本地 llama.cpp 添加一个 HTTP 代理层。

目标：

1. 对 Cursor、Codex、Claude Code 和自有应用提供稳定、受控的网关入口。
2. 统一管理调用方 API Key、权限、限流、请求取消、超时和审计。
3. 同时接入本地模型（llama.cpp、Ollama）和远程模型服务（例如 OpenAI、DeepSeek，以及后续支持的其他厂商）。
4. 在不改变模型实际回答的前提下，提供可信的响应状态、延迟、TTFT、Token 用量与错误记录。
5. 支持本地优先、远程按需选择的路由策略，但不让日志、统计或管理台轮询成为推理主链路的瓶颈。
6. 保持凭据隔离、内容最小化留存和可观测性；默认不将提示词、代码和模型回答作为普通运行日志永久保存。

非目标：

- Gateway 不负责实现模型推理本身。
- 不应假定每个 Provider 都支持本地模型加载、卸载、磁盘文件管理、相同 API 协议或相同能力。
- 不应承诺对任意请求路径、任意工具类型和任意厂商实现百分之百透明兼容。兼容范围应由协议适配器和测试矩阵明确说明。

## 2. 当前架构与主流程

### 2.1 现有组成

| 模块 | 当前职责 | 设计判断 |
|---|---|---|
| `cmd/gateway` | 配置、数据库迁移、启动 HTTP 服务与退出 | 作为组合根保留 |
| `internal/core` | 固定推理 Pipeline，不依赖数据库或 net/http | 当前最值得保留的边界 |
| `internal/server` | HTTP 路由、鉴权、模型解析、运行时管理、日志观察 | 职责仍偏多，后续逐步拆分 |
| `internal/llama` | llama.cpp / Ollama HTTP 客户端和模型运行时操作 | 应演进为本地 Provider 适配器，不应成为所有厂商的万能客户端 |
| `internal/model` | 当前模型登记与持久化 | 未来需要把“模型身份”和“本地 GGUF 文件”区分开 |
| `internal/auth` | 调用方 API Key 管理与校验 | 保留；与厂商凭据严格分离 |
| `internal/ratelimit` | Redis 限流与瞬态状态 | 保留并加强原子性和失败策略 |
| `internal/audit` + `internal/store` | 异步审计队列与 MariaDB 持久化 | 思路正确，但热路径仍有同步日志处理 |
| `internal/live` | 当前请求的内存快照，供管理台实时查看 | 应设定明确内存预算和数据留存规则 |
| `web/` | 管理台与可视化 | 不应通过高频完整轮询干扰推理服务 |

### 2.2 当前请求流程

现有 HTTP 推理入口大致如下：

```text
客户端
  -> HTTP 路由 /v1/*
  -> 方法检查、Gateway API Key 鉴权、RPM 限流
  -> 读取请求体、创建超时/取消 Context
  -> core.Pipeline.Run
       Parse -> Resolve -> Stamp -> Mutate -> Guard
       -> WarmStart -> Forward -> Finish
  -> 向客户端返回响应
```

`internal/core.Pipeline` 的固定主流程是合理方向。鉴权和限流放在入口层，Provider 请求发送收敛到 Forwarder，日志通过 Observer 接入，可以减少核心流程对存储、管理后台的耦合。

但目前 `Resolve` 后的模型就绪检查与请求级 `allowModelsGuard` 的执行顺序需要调整：当前 Pipeline 先跑全局 Guard，再跑本次请求附加 Guard；自动加载模型可能发生在模型 ACL 检查之前。权限检查应先于加载、计费或任何其他有副作用的动作。

当前 HTTP 推理会忽略请求体里的 `model`，使用管理台选定的活动模型；这应视为现有产品语义，而不是 OpenAI API 的通用行为。文档、`/v1/models` 和实际路由必须保持一致。

## 3. 日志审查：会不会影响速度和准确性？

结论：**MariaDB 写入是异步的，但整个日志路径并非完全异步；在当前实现中，流式日志处理可以直接延迟响应块的 Flush，也会污染部分延迟统计。** 在低流量下，影响可能较小；在长回答、高事件数、打开正文留存或并发请求增加时，影响会放大。目前没有基准测试数据，不应凭代码推断具体会慢多少毫秒或多少百分比。

### 3.1 已有的正确设计

- `audit.Writer` 使用有界队列和后台 Worker，数据库 I/O 通常不会直接阻塞转发 goroutine。
- `core.Pipeline.emit` 会隔离 Observer panic，日志回调异常不会直接改变 Pipeline 的返回结果。
- 正文留存有开关，流式累计正文有单请求缓冲上限。
- 流式事件有数量上限；审计写入失败也会记录日志。
- 网关调用方 API Key 不应透传给上游；当前转发器剔除了相关鉴权头，再单独设置上游密钥。

这些机制应保留，但不足以证明日志对响应完全无影响。

### 3.2 审查发现与当前状态

本节区分已修复的实现缺陷、仍需实测的风险与长期优化项。基线 CI 通过不等于已完成压力测试或真实供应商端到端验收。

#### A. 流式日志处理仍需性能评估

文件：`internal/llama/client.go`、`internal/server/pipeline_forward.go`、`internal/server/pipeline_hooks.go`。

当前状态：流式代理已将上游内容写给客户端并 Flush，再调用观察回调；但 SSE 解析、Token 统计、事件构造与实时看板更新仍有同步工作。数据库异步化本身不能证明观测逻辑对每块 Flush 间隔和整体延迟没有影响。

后续应采用固定模型、固定提示词、固定并发数，比较日志配置开关前后的 TTFT、每块 Flush 间隔、P95/P99、吞吐和 RSS，并把计时采样与可丢弃的诊断事件尽量解耦。在取得可重复数据前，不将该项标记为性能验收完成。

#### B. 正文与流事件留存边界已加固，仍需补数据保留策略

文件：`internal/server/request_trace.go`、`internal/server/pipeline_hooks.go`、配置加载模块。

当前状态：完整请求/响应 Payload、原始 SSE 与内容预览已由独立配置控制；流事件限制为每请求最多 2000 条、单条原始行最多 65535 字节，原始数据与预览共享每请求 512 KiB 的累计留存预算。默认关闭正文、原始 SSE 与预览后，不应把原文或 reasoning 作为普通审计内容保存。

仍需明确数据库保留期限、清理机制，以及高并发下 live 内存快照的全局预算。上述限制降低了单请求留存峰值，但不能代替整体内存压测。

#### C. 请求体与上游响应大小限制已接入

文件：`internal/server/pipeline_http.go`、`internal/llama/client.go`、`internal/provider/openai_compatible.go`。

当前状态：推理请求体有明确的 64 MiB 上限，超过时返回 413；本地上游非流式响应通过有界读取限制大小，远程 Provider 的非流式响应和流式错误处理也有响应体上限。超限时不应把被截断的 JSON 伪装成成功响应。

仍需通过大响应和高并发测试测量内存峰值，并确认部署代理层的 body/idle timeout 限制与 Gateway 的行为一致。

#### D. 审计队列的关闭竞态与关键事件优先级已治理

文件：`internal/audit/writer.go`。

当前状态：入队、关键容量预留和 Shutdown 通过同一生命周期锁协调；关键请求创建/最终状态优先于普通诊断任务；队列满和关闭后的丢弃有计数与日志。该实现避免向已经关闭的 channel 发送，但在数据库长时间不可用或队列持续满载时，普通任务仍可能被丢弃。

后续需将队列长度、排队时间、写入失败和丢弃量接入可采集指标，并以数据库故障、满载和退出期间仍有在途请求的场景进行压力测试。

#### E. Gateway/上游状态分离与流式错误收口已接入

文件：`internal/server/response_sink.go`、`internal/server/pipeline_forward.go`、`internal/server/finish_status_test.go`、`internal/provider/openai_compatible.go`。

当前状态：请求记录分别保存 Gateway 已提交状态与上游 HTTP 状态。远程 Provider 流式调用遇到 401、429、503 等非 2xx 时：
- 如果 Gateway 尚未提交响应，返回原上游 HTTP 状态和脱敏 JSON 错误体，不把供应商原始错误正文或凭据上下文传给客户端。
- 如果状态行已经提交（例如 warm-start 已发送 SSE 注释、客户端因此收到 200），HTTP 状态无法再改；适配器会追加安全的 SSE `event: error`，并记录真实上游错误状态。
- Chat Completions 继续使用 `data: {"error": ...}` 形状；`/v1/responses` 使用顶层 `type/code/message/param/sequence_number` 错误事件。序号观察只读取已转发的 SSE 数据，不改写有效负载；观察器的行/事件缓存有硬上限。协议依据：[Responses API 流式事件](https://developers.openai.com/api/reference/resources/responses/streaming-events)。
- 传输中断也用稳定的流式错误事件收口；不会自动重试已经开始输出的流，避免重复生成内容。

现有单元测试覆盖主要分支，但真实供应商、Codex/SDK 的实际解析和代理链路仍需端到端验证。

### 3.3 推荐的日志分层

| 级别 | 内容 | 默认策略 | 对推理路径的要求 |
|---|---|---|---|
| L0：请求结果 | request_id、模型/Provider、HTTP 状态、错误码、耗时、Token 来源 | 始终记录 | 热路径只写轻量状态；异步持久化 |
| L1：性能事件 | 首字节、TTFT、阶段耗时、重试/限流/取消事件 | 始终记录关键字段；详细事件有上限 | 轻量时间戳；禁止在 Flush 前做大块解析 |
| L2：协议诊断 | SSE 事件类型、finish_reason、usage 原始结构摘要 | 可配置、可采样 | 不记录完整内容，必须有总字节预算 |
| L3：请求/响应正文 | Prompt、代码、模型答案、完整 SSE | 默认关闭，按请求临时开启 | 与常规审计分离、加密、限额、到期删除 |

日志应遵循“**先记录发生了什么，再异步补充诊断细节**”。任何统计和观察逻辑都不能改写上游响应、重排响应内容或决定请求成败。

## 4. 扩展外部大模型服务的目标架构

### 4.1 核心原则：抽象推理能力，不强迫所有后端共享模型管理能力

当前 `internal/llama.Client` 同时承载推理转发和本地模型管理，后端类型仅包括 llama.cpp 和 Ollama。GPT、DeepSeek 等远程服务通常不需要 Gateway 加载/卸载本地文件，也不一定支持相同协议或完整的模型运行时查询。

建议逐步引入以下逻辑模块，迁移可以分阶段完成，不要求一次性重写项目：

```text
HTTP/API 层
  -> 认证与调用策略
  -> 请求规范化 + 模型路由
  -> Provider Registry
       -> Local llama.cpp Adapter
       -> Local Ollama Adapter
       -> OpenAI-Compatible Adapter
            -> OpenAI
            -> DeepSeek
            -> 其他兼容厂商
       -> 后续：Anthropic / 其他协议 Adapter
  -> 统一响应观察器与结果日志
  -> 客户端
```

外部厂商接入初期，优先选择提供 OpenAI-compatible Chat Completions 接口的供应商，通过配置不同的 Base URL、模型 ID、鉴权方式和能力声明完成接入。不能据此假设 Responses、Anthropic Messages、工具调用、视觉或 reasoning 字段都可以直接通用，必须逐项验证。

### 4.2 建议的接口边界

建议将“推理”和“本地运行时操作”拆成两类接口：

- **Inference Provider**：发送请求、支持流式和非流式响应、返回上游状态/头/usage/错误、支持取消和超时。
- **Local Model Manager（可选能力）**：本地模型扫描、加载、卸载、下载、内存快照。
- **Capabilities**：声明支持的协议、Chat Completions、Responses、工具调用、流式、视觉等能力。
- **Model Router**：根据逻辑模型 ID、Provider、调用方 ACL 和策略决定实际路由。
- **Credential Store**：安全获取上游 API Key。调用方 Gateway Key 与厂商 API Key 必须是两个完全不同的凭据域。

外部 Provider 不应为了满足本地管理接口而实现空操作的 Load/Unload；应通过能力声明决定管理台显示哪些操作。

### 4.3 模型与 Provider 配置

当前 `settings.Upstream` 表示单个活动后端，配置中的类型被限制为 `llamacpp` / `ollama`。若将来只是不断往这里增加 GPT、DeepSeek 等值，会把单活动后端模型逐渐变成难维护的厂商分支。

建议在数据库逐步建立以下概念（具体表结构应在编码前定稿）：

- `providers`：Provider 类型、显示名、协议、基础 URL、启用状态、超时与能力配置。
- `provider_credentials`：凭据引用、密文、轮换与状态信息；不要把明文密钥放进普通 Provider JSON、日志或管理 API 响应。
- `models`：稳定的 Gateway Model ID、Provider ID、远程模型 ID、显示名、能力、启用状态。
- `routing_policies`：默认路由、调用方允许使用的模型、Provider 优先级及明确的回退策略。

本地 GGUF 路径、量化格式、下载状态属于本地模型扩展信息，不应成为远程模型记录必填字段。

### 4.4 Provider 接入安全

- 厂商凭据只在网关到上游的服务端请求中注入；永远不返回给客户端，也不出现在 URL、错误正文、日志或实时请求头详情中。
- 外部 Provider 的 URL 只能由受信任管理员配置；限制 scheme 和目标地址，防止 SSRF、内网探测以及误把密钥发送给不可信主机。
- 公网厂商通常应要求 HTTPS，并限制重定向行为，避免 Authorization 头泄漏到非预期域名；自定义 Transport 必须保留 TLS 校验和 HTTP/2 能力，并对 DNS、TLS 握手、响应头等待及连接池设置边界。
- `max_response_bytes` 默认 32 MiB，最大 512 MiB；绝对上限用于避免异常配置造成整数溢出或无界内存读取。BaseURL 可包含自定义代理前缀，例如 `/openai/v1`，适配器必须避免重复拼接 `/v1`。
- 对每个 Provider 配置独立超时、并发上限、RPM/Token 限制和错误分类；厂商 429、超时、余额不足、模型不存在等错误应映射为稳定的 Gateway 错误码。
- 自动重试只能在确认没有向客户端提交任何响应内容时进行。SSE 已开始后不能随意换厂商重试，否则会造成重复输出、错误的 Token/费用统计或破坏工具调用。
- 计费与成本统计应基于 Provider 返回的 usage 和定价版本；没有可信 usage 时明确标注未知或估算，不可伪造精度。

### 4.5 活动 Provider 的健康检查语义

健康检查应跟随当前活动后端，而不是无条件探测某个历史本地客户端。切换到远程 Provider 时，网关可能继续保留 `llama.Client` 供本地模型管理 API 使用；这个对象并不代表当前推理路由。否则本地旧服务停机可能导致远程推理实际可用时 `/health` 却报降级，或本地服务正常而远程 Provider 未注册时错误报告正常。

对于本地活动后端，检查对应的本地客户端；对于远程活动后端，启动依赖检查应验证配置和 Provider Registry 已就绪，并明确写出“网络连通性在实际推理时验证”，不能将已配置等同于可达。健康响应应保留既有 `status`、`llama`、`redis` 字段，并额外暴露活动后端与 Provider 状态，以便监控系统区分本地服务健康和远程配置状态。

### 4.6 请求级 Provider 身份固定

一次推理必须在模型解析阶段捕获活动 Provider 身份，之后的请求体模板改写、采样参数合并、模型自动就绪检查和实际转发都沿用该身份。不得在 pipeline 后续阶段重新读取可变的“当前活动 Provider”，否则管理员在请求进行期间切换后端时，可能把旧 Provider 的模型 ID 交给新 Provider。

本地 Provider 也必须按自身 ID 解析并缓存独立的 HTTP 客户端。仅保存 Provider ID 还不够：如果本地 Registry 工厂每次解析时仍读取当前的 `llamaClient()`，切换 llama.cpp/Ollama 后仍会拿错客户端。客户端按后端 ID 和 BaseURL 缓存；地址变化时只替换对应缓存，管理台原有活动客户端入口保持兼容。

内部 benchmark 等进程内请求可以通过专用字段固定 Provider；旧内部调用未提供该字段时继续沿用既有行为。请求触发的自动加载不能把迟到的旧模型加载结果写回当前路由配置；管理员切换与模型自动采纳需要在配置提交阶段互斥，但不能在实际网络加载期间持锁，否则加载成功回调可能死锁。

### 4.7 请求取消与最终状态一致性

管理端取消、客户端断连、网关超时与上游完成可能并发发生。请求状态需要一个明确的线性化点：在 Finish 开始后应拒绝新的管理端取消；若取消先被接受，即使上游恰好同时返回成功，也要把本次调用记为 cancelled。此顺序同时由请求状态和 run registry 保存，防止 callState 清理期间迟到取消绕过 Finish 状态。

审计落库也要遵守同一终态规则。管理端取消可以直接把数据库中的 `running` 行更新为 `cancelled`，而推理 Finish 通过异步审计队列写入；`FinishRequest` 必须保留已经写入的 cancelled 状态、HTTP 状态与取消原因，不能让迟到的成功记录覆盖掉取消结果。内存里没有活动请求时，取消前先冲刷审计队列，以区别“Finish 尚未落库”和“进程重启后留下的 running 行”。指标字段仍可补充，以保留响应字节和 Token 等诊断信息。

 字符不被误展开；环境变量中的特殊字符仍被解析成单个标量；缺失必需密钥时 `Config.Load` 返回字段级错误；禁用 Provider 和非活动后端的可选配置不被错误地强制要求。错误信息不能包含被展开的密钥原文。

### 4.9 凭据型 Header 与查询参数的审计脱敏

客户端请求头和实际发给上游的请求头在写入实时看板或审计 payload 前必须先做统一脱敏。除了标准的 `Authorization`、`api-key`、`x-api-key`、Cookie 等字段，还需要识别厂商自定义的密钥头，例如 `X-DeepSeek-Key`、`X-Gemini-Key`。不要把所有以 `Key` 结尾的普通字段都打码，否则会失去 `X-Model-Key` 等诊断信息。

请求 URL 的查询参数只用于审计展示时脱敏；实际发给 Provider 的 `RawQuery` 不应被改写。除常见的 `token`、`api_key`、`client_secret` 和签名字段外，厂商定制的 `*-key` 查询字段也应遵循同一凭据命名识别规则。回归测试需要同时验证秘密值被遮盖、参数顺序与普通诊断字段保留、原请求转发不受影响。

### 4.10 JSON 与 SSE 上游错误脱敏一致性

远程 Provider 的非 2xx 响应不得因为 `stream=false` 就直接回传供应商原始错误正文。原始错误 JSON 可能包含内部调试上下文、凭据回显或供应商内部标识。非流式 JSON 与流式 SSE 应遵循同一错误策略：按 HTTP 状态分类，保留上游状态码和白名单响应头（例如 `Retry-After`），返回网关固定的安全错误正文，设置 `Cache-Control: no-store`，并将真实状态与错误分类写入审计记录。

回归测试需覆盖 401、429、5xx，检查上游私密错误字符串不会出现在响应中，同时验证安全错误码、状态码、Retry-After 和响应字节统计保持一致。成功的 2xx JSON 响应仍原样转发。

### 4.11 本地推理 BaseURL 凭据边界

本地 `LLAMA_CPP_BASE_URL` 和 `OLLAMA_BASE_URL` 不能通过 URL userinfo（如 `user:password@`）、查询参数或 fragment 携带凭据。BaseURL 会进入启动日志、管理状态或连接诊断，因此这类组件可能把 URL 内的秘密暴露给日志或管理界面。配置加载阶段应拒绝非绝对 HTTP(S) 地址、空主机、userinfo、query、fragment，以及反斜杠/点段路径；错误只指出配置字段，不回显 URL 原文。

URL 路径前缀仍然合法，例如 `http://llama-cpp:8080/openai/v1`。空的非活动本地后端允许不配置；一旦非空，就必须符合 URL 安全约束，避免后来切换后端时才暴露问题。相关测试应覆盖活动/非活动 URL、userinfo/query/fragment、错误 scheme、合法路径前缀和“无不安全 URL 字符串出现在错误中”。

## 5. 建议整改路线

### P0：请求安全与日志正确性

1. 严格请求体大小校验，超过限制返回 413；为非流式响应设置明确的资源上限。
2. 将模型 ACL 检查提前到自动加载或任何有副作用的操作之前。
3. 明确日志留存策略：关闭正文时也不得意外持久化原始 SSE / reasoning / 内容预览。
4. 修正审计队列的停止/入队竞态，明确队列满载下哪些记录可丢、哪些必须保留。
5. 统一客户端 HTTP 状态与上游 HTTP 状态的记录，定义 TTFT/总耗时等指标。
6. 在 `/v1/models` 中按当前模型路由和调用方权限返回一致的列表。

### P1：代理质量和运行可靠性

1. 转发请求时正确保留查询参数，并验证 Header 白名单、压缩、Content-Type、SSE、取消传播及响应头处理。
2. 修正对不同后端执行固定 llama.cpp 操作的逻辑；后端管理操作必须检查实际 Provider 能力。
3. 将 Redis 限流的计数和 TTL 设置改为原子操作，制定 Redis 不可用时的策略。
4. 对 SSE 原始事件设置每请求总字节上限；降低每块同步解析和复制开销。
5. 增加审计队列积压、丢弃、写入延迟与请求日志完整率的指标。

### P2：Provider 架构演进

1. 先定义 Provider 接口、错误模型、流式结果和能力声明，不直接在 Handler 里增加厂商 if/else。
2. 将本地 llama.cpp / Ollama 适配到该接口，并把本地模型管理能力拆开。
3. 增加 Provider / Model / Credential 的安全配置与数据库迁移。
4. 先接一个 OpenAI-compatible 供应商进行验证，再接第二个供应商验证抽象是否真实通用；DeepSeek 可作为其中一个候选。
5. 将供应商能力差异和协议兼容矩阵放入文档及自动化测试，而不是依赖口头说明。

### P3：运维、性能和体验

1. 加入 CI：`go test ./...`、`go vet ./...`、构建和关键集成测试。
2. 增加 Provider 延迟、成功率、429/5xx、Token 使用、成本和限流指标。
3. 完善凭据轮换、到期删除、数据清理、异常告警和备份恢复。
4. 通过配置控制日志级别与采样率，并对完整正文诊断实行短期、逐请求开启。

## 6. 最低测试与验收标准

### 日志与性能

- 对相同请求、相同模型、相同并发分别测量日志关闭、仅元数据、开启 SSE 事件、开启正文留存四种情况。
- 记录 TTFT、首正文时间、P50/P95 总延迟、输出速率、CPU、峰值 RSS、GC/分配量、队列积压与事件丢弃量。
- 日志开启不得改变响应正文、工具调用参数、finish_reason 或客户端可见流顺序。
- 在固定机器和模型上建立基准值，日志带来的性能回归应按预先约定阈值验收，不能只凭主观体感。
- 日志队列满、MariaDB 短时断开、Redis 断开、请求取消和进程优雅退出时，不得 panic；关键请求结果应具有明确的一致性策略。

### 兼容性与权限

- 模型 ACL 拒绝发生在本地模型加载、远端调用与计费之前。
- `/v1/models` 只暴露当前调用方可用的模型。
- 验证非流式/流式、错误状态、上游 429、超时、断连、部分响应及重试行为。
- 逐一测试 Chat Completions、Responses、tools/function call 和每个已声明支持的供应商协议。
- 验证厂商 API Key 不会被客户端提供的 Header 覆盖、不会透传到另一个域名、不会出现在日志或管理台响应。
- Provider 配置、密钥轮换、模型路由变更应有审计记录且具备回滚能力。

## 7. 最终技术决策

建议按以下顺序执行：

1. **先修正日志热路径、内容留存和审计可靠性**，因为日志是每一次请求都会经过的通用能力。
2. **再统一 HTTP 状态、TTFT、Token 来源与权限语义**，保证性能面板和错误诊断可信。
3. **然后引入 Provider / Model / Credential 抽象**，优先接入一个 OpenAI-compatible 供应商作为验证。
4. 最后再加自动回退、预算、成本和更复杂的多厂商策略。

不要在日志热路径还会延迟 Flush、内容留存策略还不清晰、审计队列还可能丢关键记录时，直接开始大量接入供应商。否则每多接一种 API 协议，排查错误和验证调用结果都会更加困难。


## 多 Agent 独立入口

多 Agent 入口由配置驱动，不要求管理员每次先切换全局活动上游。`agents` 中每条记录指定 `path_prefix`、`provider_id` 和 `default_model`，例如 `/deepseek/v1/chat/completions` 与 `/ollama/v1/chat/completions` 可同时访问。

入口层只绑定经过配置校验的 Provider，之后复用同一鉴权、限流、模型 ACL、请求生命周期、审计及转发 Pipeline。外部请求不能通过 Header/body 改变路由 Provider；Agent 路由不修改全局 `settings.Upstream`。原有 `/v1/*` 继续指向管理台当前选择的 Provider。

首版每条入口固定一个默认模型。需要单入口下暴露多个模型时，应先设计显式别名/映射和对应的 API Key ACL，不直接信任调用方任意模型字符串，也不在流式响应提交后自动故障切换。


### 当前配置方式与兼容边界

`agents` 是进程启动时加载的静态路由配置，配置改动需要重启/重新部署。每个入口当前只绑定一个 Provider 与一个默认模型；客户端请求里的 `model` 不能切换到另一个 Agent/Provider。当前复用同一 Gateway API Key、限流和 `allowed_models` ACL，没有为每个 Agent 配置独立密钥的能力。

### 远程流式错误语义

除 Anthropic Messages 的协议兼容例外外，上游错误正文可能含内部诊断信息或凭据上下文，不直接转发。未提交响应头时，网关保留上游状态码并按协议返回错误；warm-start 或上游首块已提交状态后，HTTP 状态不可逆，只能追加适配对应协议的 SSE 错误事件。Responses API 的追加错误事件维持其所需的 `sequence_number`，且已有测试验证这一收口行为。

### 多 Agent 拒绝请求的审计 URL（已修复并通过 CI）

Agent 入口在 Handler 层剥离路径前缀，再复用标准 /v1/* 处理逻辑。鉴权失败、限流拒绝与请求体超限等提前退出分支也必须使用可信路由 Context 恢复原始对外 URL。提交 275a70e 修复了拒绝请求日志此前丢失 `/deepseek` 等入口前缀的问题；Actions #79 的回归测试同时验证路径恢复和 token 查询值脱敏。该处理只影响审计展示，不改写真实转发路径或查询参数。


## Anthropic Messages 兼容边界

远程 Provider 将 `POST /v1/messages` 与可选的 `POST /v1/messages/count_tokens` 作为 Anthropic Messages 请求透传。请求的 `anthropic-*` 头按开放集合转发，不对 beta/新版本头做固定白名单；Provider 凭据仍只使用服务端配置的 `api_key`，调用方 `Authorization` 与 `X-API-Key` 不会作为上游凭据透传。模型字段依然由 Agent/上游配置固定并覆盖。Anthropic Messages 请求体限制为 32,000,000 字节，超限返回 413 `request_too_large`。

Claude Code 依赖原始上游错误 message 识别部分能力拒绝并自动降级，所以 **仅 Anthropic Messages 路径**在上游已经返回 HTTP 错误且 Gateway 尚未提交响应时，会保留原始 HTTP 状态、错误正文和必要响应头；其他协议仍使用统一脱敏错误。该兼容性例外意味着部署者必须信任配置的 Anthropic-format 上游，不能把不可信服务配置为 Provider。连接超时等没有上游错误正文的情况仍返回网关生成的安全 Anthropic 错误信封。

Claude Code 还使用 `x-should-retry`、`retry-after`、`request-id` 和 `anthropic-ratelimit-unified-*` 响应头；这些在该路径上按白名单/前缀转发。为避免流式请求提前提交 200 并丢失上游 4xx/5xx 错误，Anthropic Messages 路径不发送 Gateway warm-start SSE 注释。网关生成的入口拒绝及 Pipeline 错误也使用 Anthropic 错误信封，并按 HTTP 状态区分 `permission_error`、`billing_error`、`request_too_large`、`overloaded_error` 等类型。

当前只完成 HTTP/协议层测试，尚未完成真实 Anthropic/Claude Code 端到端验收。Claude Code 的 `HEAD /api/hello` 启动探测可以被拒绝；可选 count_tokens 端点已透传，但实际 token-count 结果依赖上游实现与配置。


### 最近一次 Anthropic 兼容性回归 CI

提交 `b3ec270421daad654c450b20c43782297f882f3f` 对应 GitHub Actions #85（运行 `39896239166`）通过全部流水线：Go 单测、`go vet`、Gateway 构建、管理台源码构建和前端部署 bundle 校验。该 CI 覆盖 HTTP/协议级 Mock 测试；真实 Anthropic/Claude Code 端到端、DNS/连接超时故障注入与在线能力降级仍未验收。


## CI 性能基线（首轮样本）

Actions #94 记录了以下首轮值（Go 1.23.12、linux/amd64、AMD EPYC 7763 Runner）：Provider loopback Mock 转发 90,352 ns/op、15,795 B/op、146 allocs/op；Agent 路由模型解析 342.0 ns/op、0 B/op、0 allocs/op；RequestAnalysis 构建 1,372 ns/op、592 B/op、17 allocs/op。Artifact 保留 30 天：[下载基准数据](https://github.com/Kasign/llm-gateway/actions/runs/39898164924/artifacts/12477692434)。

Provider 测量使用 loopback Mock，不包含真实供应商网络 RTT 和模型推理。单次 CI 数据只建立比较基线，不证明优化效果，也不代替压力测试或生产 SLO。


### ResponseHeaderTimeout 回归验收

提交 `3a9d56021c5eba0066287eccace7b3d7b499bbe8` 的 Actions #96（运行 `39898522951`）通过超时回归测试和完整 CI。测试通过本机 Mock 延迟响应头触发 HTTP Transport 的 `ResponseHeaderTimeout`，验证 OpenAI-compatible 请求映射到 504 `provider_timeout`，Anthropic Messages 返回 504 `timeout_error` 信封，并检查对外错误未泄露目标 URL 或原始 Transport 诊断。该测试不代表真实公网供应商端到端 SLA。


### Agent data-plane 集成验收

Actions #99（提交 `4ef97c9d9f748db1fd4aa23312ab728a5a1b49f4`）通过 `TestAgentRoutesForwardToDistinctConfiguredProviders`。它通过真实 Agent 前缀剥离、`core.Pipeline`、Provider Registry 和 HTTP 出站请求，分别走两个 loopback Mock 上游，验证调用方 model 无法覆盖 Agent 默认模型、query 保留、两路 Provider 凭据互不串用，且 Gateway 调用方 `Authorization`/`X-API-Key` 不会作为凭据转发。测试绕过 API Key/Redis 限流与数据库审计观察器；这些是另外的边界，不应把该测试描述为公网/真实供应商 E2E。


### Agent SSE data-plane 验收

提交 `4e7e134a7a19fbe3dc1b2efb05f4383c0bcff978` 的 Actions #101 通过双 Agent JSON/SSE 往返测试。流式请求通过相同的 Agent 路由层、Pipeline 和 Provider HTTP adapter；测试确认响应 content type 为 `text/event-stream`、事件内容来自对应上游并以 `data: [DONE]` 收尾，服务端模型和 API Key 不串线，调用方 Authorization/X-API-Key 不泄漏。上游为 loopback Mock，不代表真实供应商 E2E。


### Agent 模型发现与 ACL

Agent 的 `GET /<agent-prefix>/v1/models` 返回的不是上游任意模型目录，而是该 Agent 配置的固定 Provider/默认目标模型（或由已解析目标给出的模型 ID），并调用同一套 `modelAllowedForKey` 检查调用方 Key 对公开模型 ID、上游模型 ID 或提示名称的 ACL。当前新增纯函数单测覆盖放行与拒绝语义；它不代替外层 API Key/Redis 限流/审计的 HTTP 集成测试。


### Agent 模型发现与 ACL 回归验收

Actions #104（提交 `65d8709ff2fdad3a1c9e59c2b9d300a173f4cac5`）通过 Agent 模型列表 helper 单测，覆盖受限/不受限 Gateway Key、按公开 ID/上游 ID 授权、ACL 不匹配为空列表。Handler 现在调用同一个纯函数产生 list body 与过滤后数量，审计日志不再引用已移入 helper 的局部变量。此测试不替代真实 HTTP Handler 的鉴权、Redis 限流与审计全链路验证。


### Anthropic Messages 正向路径测试（待 CI）

新增 Mock Provider 测试覆盖 Messages SSE 成功响应事件序列与可选 count_tokens 端点：用于验证远程 Provider 的实际 HTTP path、query、anthropic-version、model 覆盖和服务端 API Key 隔离。由于上游为 loopback Mock，这些测试仅验证协议适配器，不表示真实 Anthropic/Claude Code 已端到端验收。


### Anthropic Messages 成功流式与 count_tokens（CI #106）

提交 `4e5d69766e9a16a668e2398b918def97cb64d3f2` 的 Actions #106 已通过完整 CI。新增测试验证：
- `/v1/messages` SSE 成功路径透明转发完整的 message_start、content_block、message_delta 和 message_stop 生命周期事件。
- Anthropic-Version、查询参数、Provider 服务端 API Key、model 覆盖、响应 Request-Id 透传和调用方 Authorization 隔离。
- `/v1/messages/count_tokens` 的请求路径、query、模型覆盖、Anthropic-Version、Provider Key 和 JSON 响应透传。
- 这些测试以 loopback Mock 为上游，不代表真实 Anthropic/Claude Code 供应商 E2E 已完成。


### Agent /v1/models 外层鉴权与 Redis 限流验收（CI #108）

Actions #108 通过实际 `Server.Handler()` 集成：缺失/未知 API Key 返回 401；已鉴权 Key 的模型列表受 AllowedModels 控制；通过真实 Redis 服务验证 RPM 超限返回 429。该测试的 API Key/审计 SQL 使用 test-only driver，仅模拟查行与写入成功，不代表 MariaDB schema/方言的端到端验收。Artifact：[性能基线文件](https://github.com/Kasign/llm-gateway/actions/runs/39904722392/artifacts/12480765287)。


### 真实 OpenAI-compatible Provider E2E（显式 opt-in）

为在不把密钥放入仓库/聊天记录的情况下验证真实供应商，仓库提供 `TestOpenAICompatibleProviderRealEndpointE2E`。默认跳过；只有设置 `LLM_GATEWAY_PROVIDER_E2E=1` 才发起网络请求。请在本机 shell 临时设置 `LLM_GATEWAY_E2E_BASE_URL`（HTTPS `/v1` Base URL）、`LLM_GATEWAY_E2E_API_KEY`、`LLM_GATEWAY_E2E_MODEL`，必要时设置 `LLM_GATEWAY_E2E_API_KEY_HEADER`（允许值由 Provider 配置校验约束，留空为默认 Authorization）。随后运行：

```sh
go test ./internal/provider -run '^TestOpenAICompatibleProviderRealEndpointE2E$' -count=1 -v
```

测试只发出一条短 Chat Completions 请求（最多 8 个输出 token，90 秒超时），不打印 API Key、原始上游错误正文或响应内容。不要把真实密钥写入 shell 历史、仓库文件或 CI 日志；未明确开启时测试不产生任何网络请求。当前 CI 未配置真实供应商密钥，所以只能验证该测试编译及默认跳过，真实端点结果仍需在用户控制的环境执行。


### Responses API JSON/SSE 成功契约（CI #114 已通过）

新增 `TestOpenAICompatibleProviderForwardsResponsesJSONAndSSE`，使用 loopback Mock Provider 覆盖成功 JSON 与 SSE。该用例检查客户端 model 由网关选择的模型 ID 覆盖，出站 `OpenAI-Beta` 与 query 透传，Provider 的 Authorization 凭据与调用方 `Authorization`/`X-API-Key` 互相隔离，以及 Response-Id/Request-Id 和原始 SSE 序列。Actions #114 已通过这两条 Mock HTTP 契约测试；真实 OpenAI Responses/Codex 端点仍需单独验证。


### Responses API JSON/SSE 测试验收（CI #114）

提交 `d8bcafee44ccbb7a4a5bd459bf363563ccbb1e1a` 的 Actions #114（运行 `39906018427`）通过 `TestOpenAICompatibleProviderForwardsResponsesJSONAndSSE`。该测试覆盖成功 JSON 与流式 SSE，验证路径/query、`OpenAI-Beta`、`Accept`、stream 标志、服务端 model 覆盖、Provider Authorization 与调用方凭据隔离、Request-Id 和响应内容透明转发。仍未用真实 OpenAI/Codex 密钥执行真实公网 E2E。


### Anthropic Messages 真实端点 E2E（显式 opt-in；CI #116 已验证默认跳过路径）

仓库另提供 `TestAnthropicMessagesRealEndpointE2E`，用于在用户控制的环境验证真实 Anthropic Messages 上游。默认跳过，需显式设置 `LLM_GATEWAY_ANTHROPIC_E2E=1`，并提供：
- `LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL`（HTTPS Base URL，通常以 `/v1` 结尾）
- `LLM_GATEWAY_ANTHROPIC_E2E_API_KEY`（仅本地环境变量，不应提交到代码）
- `LLM_GATEWAY_ANTHROPIC_E2E_MODEL`

执行命令：

```sh
go test ./internal/provider -run '^TestAnthropicMessagesRealEndpointE2E$' -count=1 -v
```

该测试发送一次短非流式 Messages 请求，固定使用 `x-api-key` 和 `Anthropic-Version: 2023-06-01`，请求最多 12 个输出 token，超时 90 秒。它不打印原始错误正文或响应内容。提交 `594ef12048e0a9dee52bb6c1ae4d780f1cbfaa3a` 对应 Actions #116（运行 `39906558311`）全量 CI 成功，验证了测试编译和默认跳过路径；因未提供真实 API Key，真实 Anthropic 服务尚未被调用。


### Anthropic E2E 测试夹具验收（CI #116）

提交 `594ef12048e0a9dee52bb6c1ae4d780f1cbfaa3a` 的 Actions #116（运行 `39906558311`）通过全部 CI，包含新增 `TestAnthropicMessagesRealEndpointE2E`。该用例默认跳过网络访问；当前没有配置真实 Anthropic API Key，因此这只验证了测试编译和安全的默认跳过路径，不代表真实供应商端点已通过。


### OpenAI Responses 真正端点 E2E（显式 opt-in）

可使用 `TestOpenAIResponsesRealEndpointE2E` 验证真实 Responses API JSON 往返。先按 OpenAI-compatible E2E 的方式设置 BaseURL、API Key 和模型 ID，再单独开启 `LLM_GATEWAY_RESPONSES_E2E=1`：

```sh
go test ./internal/provider -run '^TestOpenAIResponsesRealEndpointE2E$' -count=1 -v
```

测试访问 `/v1/responses`，请求限制为 8 输出 token，90 秒超时；仅打印状态与响应字节数，不打印密钥/响应正文。默认 CI 不发起请求。该用例仅适用于支持 Responses API 的真实端点，不应把普通 Chat Completions 兼容性等同于 Responses 能力。

## 调用记录按 Provider 精确筛选

管理台「调用记录」允许输入任意 Provider ID，并与模型、状态和 API Key 名称条件组合筛选。示例：

```text
GET /api/requests?limit=100&provider=deepseek
```

筛选不是包含匹配：服务端对输入执行 `TrimSpace + ToLower` 后，将其作为 `request_logs.provider_id = ?` 的参数；`deep` 不会命中 `deepseek`。Provider ID 在创建和结束请求时也会规范化为小写，因此普通查询可以保持对索引列的直接比较，避免对列套用 `LOWER()` 而损失索引使用机会。空值不会附加 Provider 条件；历史上没有来源身份的行仍保留为空，选择具体 Provider 时不会被猜测性归类。

调用列表有三秒短期缓存。缓存键必须同时包含 limit、request ID、模型、状态、Key 名和 Provider ID，否则不同筛选条件可能复用同一组结果。Provider 条件沿 API、缓存层传入持久化查询；旧的 `Repo.Query` 方法保留并委托给不带 Provider 条件的查询，以免影响详情及既有调用方。

列表同时显示数据库历史记录和进程内 LiveRegistry 快照。运行中的请求在 Resolver 完成前可能只有候选 Provider；因此列表先按已有条件合并实时快照，再按最新的 `provider_id` 过滤，并在排序/截断前过滤，避免候选身份更新后旧 Provider 视图残留，也避免不匹配的实时行占用返回条数。该实时修正仅对当前 Gateway 进程中的在途请求有效；重启后不再有 LiveRegistry 快照，最终归属以落库记录为准。

本功能只增加观测与检索能力，不改变路由选择、限流、请求执行和 Provider 权限边界。代码与文档完成后按统一安排未运行测试、构建或 CI；真实 MariaDB 查询计划、筛选与并发更新时的列表一致性需在后续统一验证。

## 调用统计的窗口、延迟和 Token 来源

统计 API 为 `GET /api/requests/stats?range=24h&provider=deepseek`，只接受 24 小时、7 天和 30 天的固定窗口。窗口按 `[from,to)` 计算并使用 `started_at` 归属；可选 Provider 条件按规范化的 ID 完整匹配。统计为管理员接口，不返回单条请求、正文、Header、Key 或原始上游错误。

数据库执行一次聚合查询：时间范围先在内层查询过滤，并复用 `idx_started(started_at)`；指定 Provider 时使用 `provider_id` 条件并可利用 `idx_provider_started(provider_id,started_at)`。SQL 汇总状态数量、平均延迟、六档延迟直方图和来源分组 Token 总量，不把窗口内所有请求行拉回 Go 进程。成功率定义为 `success / completed`，已取消请求属于已结束请求分母；延迟只纳入已结束且有耗时值的调用。

Token 来源必须与数值分开判断。网关把 `analysis_json.throughput.source` 为 `usage/upstream_usage` 的记录归到上游 usage；`llama_timings` 代表本地 llama.cpp timings 计数，不应当成供应商计费 usage；未知或旧记录来源归入 `unknown_source`，即使有 Token 数字也不猜测来源。分析 JSON 是异步审计写入的一部分，极短时间内统计结果可能暂时把尚未写入来源标记的记录算入未知来源；之后重新查询即可看到最终分类。Token 汇总使用审计行已经记录的数字，不读取 Payload 重新解析，也不回写历史记录。

管理台调用记录页增加统计窗口和聚合卡片；统计仅受时间范围与 Provider ID 过滤，不继承模型、状态和 Key 名称筛选。横条展示延迟数量区间，不伪称为精确的 p50/p95 分位数。


## 机器可读 API 契约与版本边界

当前的 [`docs/openapi.yaml`](openapi.yaml) 是首阶段机器可读契约，针对已稳定的公共推理入口、请求统计与 Web Provider 管理接口。规范标记为 partial，避免客户端误以为尚未纳入的历史管理 API 已经完成字段承诺。

扩展契约时以路由注册、Handler 方法检查、实际 JSON 字段和认证辅助函数为依据；禁止仅根据前端调用或历史说明推断 API 行为。一个路径若支持多种协议（例如 OpenAI Chat、Responses 和 Anthropic Messages），需保留各自请求体与错误信封的边界。流式响应使用 `text/event-stream`；客户端收到响应头后，HTTP 状态已不可逆，契约必须说明 SSE 错误事件而非假定所有失败都能改成 JSON 错误。

OpenAPI 的后续阶段应补齐 API Key、模型/下载/运行时/Benchmark、推理参数和登录接口的请求/响应模式，再考虑自动检查。只有字段经代码核实且变更同步到规范后，才将相应端点标记为完整；当前不将该文档描述为自动生成或已与所有 Handler 校验一致。


## 调用方 API Key 与管理员凭据的隔离

管理台的调用方 Key 管理端点现在进入机器可读契约。设计上有三个独立凭据域：

| 凭据 | 使用位置 | 返回/持久化边界 |
| --- | --- | --- |
| 调用方 Gateway API Key（`sk-llm-*`） | `/v1/*` 与 Agent 推理入口 | 数据库只保存 SHA-256 摘要；创建和轮换时明文仅返回一次，元数据列表不含摘要。 |
| Web 管理会话 Token | `/api/*` 管理接口 | 通过 `Authorization: Bearer` 传递，服务端检查 Redis 会话和登录凭据版本；不是 Cookie。 |
| `X-Admin-Key` | 管理 API 的部署自动化调用 | 来自部署配置；不能用调用方 Key 或上游 Provider Key 替代。 |
| 上游 Provider API Key | Gateway 发往远程 Provider | 由服务端 Provider 配置读取；Web Provider 密文不回显，调用方输入的 Key 不作为上游凭据透传。 |

创建/轮换调用方 Key 的返回正文包含明文，管理台应只在此时提示用户安全保存，不能将完整返回写入普通日志、审计详情、分析事件或浏览器持久化存储。机器可读规范将对应字段标记为 `writeOnly`，表示它只适用于响应中的一次性领取，而不是后续读取凭据。


## 请求审计与运行时 API 的运维边界

- `GET /api/requests` 合并数据库与当前进程 LiveRegistry；已完成请求的最终字段以持久化记录为准。Provider 筛选在规范化 ID 后完整匹配；不能把历史空 Provider ID 推断归属。
- `GET /api/requests/{id}` 是高敏感管理端点：启用 payload 存储时，详情可能包含完整 prompt、工具参数、请求头和上游返回内容。应以管理员身份验证和部署网络边界同时保护，而不能因为接口属于只读 GET 就视为无敏感信息。
- `GET /api/admin/audit/metrics` 仅读取审计队列快照，不访问数据库，返回带 `Cache-Control: no-store` 的数据；它的计数用于发现积压和丢弃，不等于数据库历史行数。
- `/api/admin/runtime/select` 和 `/unload` 操作的是当前活动的本地推理后端，远程 Provider 不支持本地模型加载/卸载。切换或卸载前端应先核实活动 backend，避免把本地容器生命周期操作误认为供应商操作。
- Benchmark 接口采用异步任务模型，GET 查询任务与 SSE 事件快照，DELETE 发送停止信号；它与真实调用方请求的持久化审计是不同视图。进程重启会丢失内存中的历史 Benchmark Job。


## 慢模型的 Benchmark 超时语义

`llama.request_timeout_seconds` 显式设置为 `0` 表示不添加 Gateway 整体请求 Deadline；旧配置未指定时仍使用兼容默认值。Benchmark 与普通推理必须遵守同一语义，不能将零秒交给 `context.WithTimeout`，因为那会立刻使 Context 进入超时状态。

目前 Benchmark 使用可选时限 Context：正数创建带 Deadline 的 Context，0 创建仅继承父 Context 的可取消 Context。这样长 prefill / 首 Token 等待不会被零秒配置误取消，同时用户发出的停止信号仍然可中断模型加载和推理。该改动不影响 Codex、Cursor 等客户端自身设置的等待时限；客户端断开后父请求链的取消语义仍需要保持。

## 高负载下的请求生命周期审计写入

一次请求创建记录时，`CreateRequest` 已直接写入已解析的 `model_requested`、`stream` 和候选 Provider。不要再对相同字段执行无意义的紧邻 UPDATE。Resolver 确定路由后，Provider ID 与实际上游模型通过一条关键审计更新一起持久化；这样可在在途记录查询中保持来源一致，同时减少单 Worker 队列的 SQL 次数。请求结束时，`FinishRequest` 仍是最终权威状态与指标更新。

审计队列保持有界，非关键分析数据在队列压力下仍可丢弃并计入指标；关键生命周期任务不能因为冗余数据库更新被不必要地放大。Gateway 负载验收必须同时检查 HTTP 响应、持久化行数、终态、Provider ID 和实际模型字段，不能只看吞吐，也不能通过扩大队列或降低断言来掩盖审计丢失。

## 管理统计接口的错误边界与 HTTP 状态码

请求统计接口在 MariaDB 聚合查询失败时，服务端日志保留实际错误用于排障，但 API 只返回稳定的 `database_error` 错误码与通用提示，不直接暴露驱动错误、表/列信息或数据库诊断文本。新增管理 API 时应区分运维日志和调用方错误契约。

Web Provider 管理端点的成功状态也需要与 Handler 一致：`POST /api/admin/providers` 创建成功返回 `201 Created`；`DELETE /api/admin/providers/{id}` 删除成功返回 `204 No Content`，不附带响应正文。OpenAPI 必须反映实际状态码与空正文语义，避免客户端依赖不存在的响应体。

## Provider 并发预算热更新的顺序约束

Provider 更新复用共享 `ConcurrencyLimiter`，因此限额的应用时点影响正在执行的旧实例。降低上限时必须在持久化配置前收紧准入；如果 MariaDB 保存失败，立即将许可器恢复到原上限。若 Registry 替换失败，既要恢复许可器，也要回滚持久化配置。提高上限则只创建待处理动作，必须在 Registry 替换成功后应用，避免失败窗口内短暂放宽预算。

编辑已停用 Provider 时只保存期望配置，不应改写可能仍被旧在途请求共享的运行时许可器；重新启用时，再按降低前置、提高后置的规则应用新预算。相关顺序需要有测试覆盖，不能只测试独立的并发许可器方法而忽略配置持久化与 Registry 操作之间的窗口。

## 管理 API 路由与 OpenAPI 契约覆盖范围

`docs/openapi.yaml` 现扩展覆盖模型目录/下载任务、Hugging Face 文件与搜索、Ollama Library tags、Backend 查看/切换/批量卸载/上游模型列表、Bootstrap 初始化快照、动态推理参数、Ollama Pull 和统一 model-ops 等路由。契约字段以注册路由和实际 Handler 为依据；安装模型和 Ollama Pull 都是异步任务，后续进度统一从 `GET /api/downloads` 查询。

参数表单通过 `GET /api/admin/inference-params/schema` 动态描述，因此 `params`、`policy` 以及不同后端的运行时快照保留扩展 Schema。它们不代表已承诺每个内部字段长期稳定。兼容的逐模型 load/unload 与 Ollama load/unload 路由仍保留，但新管理操作优先走 `POST /api/admin/model-ops`，并注意卸载（仅释放内存）与删除（移除磁盘模型）是不同操作。

当前 OpenAPI 依然标记为 partial：Agent 的完整协议组合、所有历史管理路由的 JSON 字段，以及动态参数与源码逐路由自动差异检查仍需后续完成。任何契约检查失败应按真实 Handler 修正规范，而非仅放宽检查或用通用对象掩盖已知稳定字段。

## 固定 Provider Agent 的协议路径覆盖

Agent 前缀路由复用同一套鉴权、请求限流、Pipeline 与审计，同时使用启动配置中的固定 Provider，不允许请求体自行选择 Provider。OpenAPI 现在覆盖 Agent 的 models、Chat Completions、Completions、Responses、Anthropic Messages 和 Messages count_tokens 路径；不同协议仍保留各自的请求和错误信封边界。

Ollama Library tags 的稳定字段包括 tag、full_name，以及可选的 size/context/input；Backend 查看接口的 available_backends 是对象数组，包含 id、label、kind、base_url、source 和 editable，可选字段包括 default_model、RPM 与 Key 是否已配置。契约优先明确这些稳定字段，只有运行时快照和目录搜索卡片等确实动态的结构才使用扩展对象。

## 路由注册与 OpenAPI 路径对账

CI 执行 `scripts/check_openapi_routes.py`，把 `Server.Handler` 中的字面路由注册与 `docs/openapi.yaml` 的顶层 path keys 对比。对于 Go ServeMux 前缀和 handler 内部 path parsing，脚本维护显式动态路由族映射，包括模型动作、请求详情/取消、Provider 操作、Benchmark 与 API Key 动作；Agent 固定 Provider 路由则按支持的公共推理协议展开。

该脚本的目标是防止新增/删除路由时漏改 OpenAPI 路径集合。它不是 OpenAPI 解析器，也不会声称验证 Handler 所有方法、状态码或 JSON 字段与 Schema 一致；这些仍需按功能修改和 Review 时同步。动态路由模式若变化，必须更新脚本的映射规则，避免用简单忽略规则绕过路径缺口。

## llama.cpp 模型目录发现：Router 与单模型模式

管理台的运行时目录和模型加载候选不应只把 HTTP 状态码 200 当作“已成功发现模型”。llama.cpp 的 Router `GET /models` 用于枚举目录中的模型（包括未加载模型）；OpenAI 兼容 `GET /v1/models` 返回当前加载模型的信息，适合非 Router 单模型服务，但不是全目录清单。

Gateway 的查询顺序为：

1. 优先采用 `/models` 返回的、至少包含一个非空 `data[].id` 的目录，保留 Router 的加载状态和目录元数据。
2. 当 `/models` 请求失败、响应结构无效或目录为空时，尝试 `/v1/models`。只要兼容接口有有效响应就解析；该接口返回的模型应显示为已加载，因为它代表当前推理实例。
3. 如果 Router 返回合法空目录，而兼容接口不可用，则保留合法空目录；只有两个端点均无法提供有效模型列表时才返回发现错误。这样既避免空结果吞掉单模型实例，也不会把 Router 中真实的未加载模型误标成已加载。
4. `Client.ListModelIDs` 与运行时快照共用这套优先级，避免管理台看得到模型、加载候选却无法解析同一个模型 ID。

部署核对时，`LLAMA_CPP_BASE_URL` 应指向 llama.cpp 服务根地址；若要列出模型目录，建议以 Router 模式运行并配置正确的模型缓存或 `--models-dir`。单模型模式只承诺展示当前加载的模型，不应视为磁盘上的完整 GGUF 清单。自动测试覆盖接口选择与状态解析，但真实容器挂载目录仍需在部署环境确认。
