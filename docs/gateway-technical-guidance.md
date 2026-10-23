# LLM Gateway 技术指导与整改路线

> 最近一次代码功能组 CI 验收：提交 `37a3ae692b6f0560f520ea5b410a40b0fdc54d6c`，GitHub Actions #81（运行 `39894814380`，Go CI，success）。该运行通过 Go 单测、`go vet`、Gateway 构建、管理台源码构建和前端语法/关键字段检查。后续代码功能组仍须由自身提交的 CI 验证。

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

上游错误正文可能含内部诊断信息或凭据上下文，不直接转发。未提交响应头时，网关保留上游状态码并返回稳定的脱敏 JSON；warm-start 或上游首块已提交状态后，HTTP 状态不可逆，只追加脱敏 SSE 错误事件。Responses API 的追加错误事件维持其所需的 `sequence_number`，且已有测试验证这一收口行为。

### 多 Agent 拒绝请求的审计 URL（已修复并通过 CI）

Agent 入口在 Handler 层剥离路径前缀，再复用标准 /v1/* 处理逻辑。鉴权失败、限流拒绝与请求体超限等提前退出分支也必须使用可信路由 Context 恢复原始对外 URL。提交 275a70e 修复了拒绝请求日志此前丢失 `/deepseek` 等入口前缀的问题；Actions #79 的回归测试同时验证路径恢复和 token 查询值脱敏。该处理只影响审计展示，不改写真实转发路径或查询参数。


## Anthropic Messages 兼容边界

远程 Provider 对 `POST /v1/messages` 按 Anthropic Messages 的请求结构透传。调用方的 `anthropic-version` 与 `anthropic-beta` 在允许列表内转发；上游凭据仍只使用 Provider 的 `api_key`，例如设置 `api_key_header: x-api-key`，不会把 Gateway 调用方的 `Authorization` 或 `X-API-Key` 透传为厂商凭据。模型字段依然由 Agent/上游配置固定并覆盖。

该路径的上游错误 JSON 与流式 `event: error` 使用 Anthropic 错误信封，并继续屏蔽厂商原始错误正文。此适配属于 HTTP/协议兼容层，不代表每项 Anthropic Beta 能力或真实 Claude Code 端到端行为都已验收；真实供应商验证仍需单独执行。
