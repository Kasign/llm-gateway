# TOOL-05 请求级 Coordinator 设计与实现

> 状态：已完成
> 更新日期：2026-11-17
> 仓库：Kasign/llm-gateway
> 分支：dev
> 开始基线：e1422acbaedadbaabacfbf14e395ad5e460ada02
> 前置任务：[TOOL-04 SearXNG Adapter](任务历史/工具能力/TOOL-04-SearXNG-Adapter.md)
> 任务状态权威来源：[整体任务规划](整体任务规划.md)

## 1. 任务目标

建立请求级 Tool Coordinator 的清晰设计和实现边界。Coordinator 管理一次非流式 Chat Completions 请求中的模型—工具多轮交互；Runtime 仍只负责单次工具执行。

本任务先审查 Provider / Pipeline 的现有响应路径，确定 Buffered Provider 可选接口、Coordinator API 和请求生命周期接入方案；不得通过递归 Handler 或多次 Pipeline.Run 实现工具循环。

## 2. 已确认且不可破坏的约束

- 只支持非流式 Chat Completions；流式工具调用另归 TOOL-10。
- 现有无工具请求必须保持原路径和行为不变。
- 工具功能关闭、Provider 不支持或策略不允许时，不向上游发送工具定义。
- 工具定义由服务端 Registry 提供；客户端请求中的 `tools` 不能注册工具、覆盖 Definition 或授予权限。
- 每轮始终使用本次请求已经解析的 Provider 和活动模型；工具结果不得触发模型切换。
- 一次客户端请求只有一个顶层 request_id、一次 Pipeline.Run 和一次 Finish。
- 中间模型响应和工具调用结果不得提前写入客户端 ResponseWriter；只返回最终模型响应。
- Coordinator 负责多轮流程和请求级预算；Runtime 负责授权复核、参数校验、超时、并发和单次 Executor 调用。
- 工具按顺序执行；第一版不并发执行同一轮多个 tool_calls。
- 不自动重试模型请求或工具执行。
- 工具调用审计持久化归 TOOL-08；本任务不新增数据库表。

## 3. 初始资源预算

- 每个请求最多 3 轮工具调用。
- 单轮最多 3 个 tool_calls。
- 单请求累计工具结果最多 48 KiB。
- 每次工具执行继续受 Runtime 默认 30 秒超时、64 KiB 结果上限和全局并发限制约束。
- 达到预算上限时停止后续工具执行，返回可诊断错误，不截断后继续编排。

## 4. 实现前审查

1. 审查 `internal/provider/provider.go`、`openai_compatible.go`、`local_http.go`：确认 Provider 现有接口、Capabilities、请求构造、响应处理和错误语义。
2. 审查 `internal/server/pipeline_forward.go`、`pipeline_http.go`、`proxy_v1_chat_flow.go`：明确当前 Provider Forward 直接写 ResponseWriter 的位置，以及怎样在不影响普通请求的前提下缓冲工具请求。
3. 明确可选 Buffered Provider 接口：只为支持非流式 Chat Completions 的 Provider 提供缓冲调用能力；不要为所有 Provider 重写接口，也不要把工具执行逻辑放进 Provider。
4. 明确 OpenAI-compatible 请求/响应类型的最小子集：messages、tools、tool_choice、finish_reason、message.tool_calls、tool_call_id、usage。
5. 明确模型工具调用响应的严格解析规则：缺字段、重复 tool_call_id、未知工具名、无效 arguments JSON、非 function 类型、超出预算均不得执行。
6. 明确工具结果如何作为 `role=tool` 消息追加，并与原 assistant tool_calls 消息保持 tool_call_id 对应。
7. 明确模型返回最终响应时如何复用原响应头、usage 和现有审计字段；不能把中间轮次当作独立用户请求。
8. 审查 Runtime Authorizer 如何从可信请求 Context 获取调用方身份；Coordinator 不读取客户端伪造的工具权限字段。
9. 先完成方案和测试切面设计，再修改 Pipeline 接入点。

## 4.1 Provider / Pipeline 审查结论与实现决策

已对照当前 `dev` 代码审查 Provider 和 Pipeline 的非流式响应路径：

- `provider.Provider` 当前统一提供 `Forward(ctx, InferenceCall) (core.Result, error)`，不需要为了工具调用改写所有 Provider 接口。
- 远程 `OpenAICompatibleProvider.forwardJSON` 在 `core.Result.Body` 返回完整响应体，同时通过 `InferenceCall.Writer` 写出响应。
- 本地 `LocalHTTPProvider` 经 `llama.Client.Proxy` 取得完整响应体并写入 Writer；同样可以在非流式模式下缓冲。
- `inferenceForwarder.Forward` 是 Pipeline 唯一出网点；`Pipeline.Run` 在退出时统一执行一次 Finish。
- 因此工具循环必须放在同一次 Forward 调用内部。不能递归调用 Handler / Pipeline，也不能把每轮模型调用拆成新的请求生命周期。

**Buffered Provider 决策：第一版不新增 Provider 公共接口。** 当前两类受支持 Provider 已经返回非流式响应体；Server 内部可提供仅捕获响应头/状态、丢弃 Writer 写入内容的缓冲适配器，并以 `core.Result.Body` 作为模型响应。这样复用现有 Provider 的鉴权、并发许可、请求改写、超时、错误映射和响应大小限制，避免重复实现 HTTP 客户端。若后续 Provider 无法满足“非流式返回完整 Body”的契约，再增加可选接口，不提前扩展。 Provider 返回上游非 2xx 且已有 HTTP 响应体时，Server 适配器必须把状态、响应头和 Body 作为正常的 `ModelResponse` 交给 Coordinator（不把它误当传输错误）；连接失败、超时等没有可转发响应体的错误仍按 Provider 错误映射返回。

Coordinator 的边界确定为：

- 放在 `internal/tools/coordinator`，仍属于唯一的 Tools 模块。
- 只依赖工具 Registry、Runtime 执行接口和抽象的非流式模型调用接口；不依赖 `server`、`provider` 或具体工具子模块。
- Server 在每次请求的 Forward 阶段解析一次 Provider，并把同一 Provider 实例、同一活动模型 ID 封装成模型调用器；所有轮次复用，禁止中途切换。
- Coordinator 接收服务端计算后的允许工具名称，只从 Registry 读取 Definition；客户端 `tools` 不参与注册或定义覆盖。Runtime 每次执行时仍重新授权。
- 只有服务端明确提供非空允许工具集合时才进入 Coordinator；普通请求继续原有 Forward 路径。
- Coordinator 负责替换模型请求中的工具定义、显式关闭模型并行工具调用、追加 assistant tool_calls 与 tool 结果消息、顺序执行、轮次预算和协议校验。工具结果使用 JSON 字符串写入 `role=tool` 消息，并保留对应 `tool_call_id`。
- 每个请求最多 3 个工具轮次、每轮最多 3 个调用；工具结果累计最多 48 KiB。3 轮工具调用后仍需再进行一次模型调用以取得最终答复，因此模型调用总上限为 4 次。
- 在执行同一轮工具调用前，先校验整轮调用数量、类型、ID 唯一性、工具名称和 arguments JSON；未知工具或格式错误不得执行该轮中的任何工具。工具按顺序执行，不并发、不自动重试。
- 非 2xx Provider 响应原样结束，不解析为工具调用；最终只向客户端写出最后一轮模型响应。

待 TOOL-06 / TOOL-08 处理的边界：多轮模型 usage 的请求级汇总与审计关联。Coordinator 应保留每轮响应的 usage 证据；不能把最后一轮 usage 误称为整个请求总量。

## 4.2 实现结果

已在 `internal/tools/coordinator` 建立独立 Coordinator 核心包：

- 定义非流式 `ModelCaller`、模型响应结构和工具执行接口；不依赖 Provider、Server 或具体工具子模块。
- 使用服务端传入的允许工具名称读取 Registry Definition，覆盖客户端请求中的工具定义。
- 支持模型—工具多轮交互、顺序执行多个 tool_calls、assistant/tool 消息追加和 `tool_call_id` 关联。
- 固定每请求最多 3 个工具轮次、每轮最多 3 个调用、累计结果 48 KiB、模型响应 32 MiB。
- 严格检查模型响应与 arguments JSON，拒绝重复键、非法 UTF-8、尾随 JSON 值和超深结构。
- 执行前校验整轮调用、工具名称、调用 ID 和参数 Schema；Runtime 仍在每次执行前重新授权。
- 执行 `tool_choice` 约束：`none` 不执行工具，`required` / 指定工具约束首轮；首轮工具结果回传后切换为 `auto`。
- 显式设置 `parallel_tool_calls=false`，确保第一版顺序执行。
- 上游非 2xx 响应与 Provider 错误同时返回时，保留状态、响应头和响应体；错误路径保留部分执行计数。
- 单元测试覆盖闭环、顺序执行、未知工具、无效参数、重复 ID、预算、取消、上游错误和 tool_choice 语义。

**范围边界：** TOOL-05 交付 Coordinator 核心及其协议契约。Buffered Provider 适配器、Server 装配、请求路由和 Pipeline 生命周期接入属于 TOOL-06，不在本任务内实现；避免将请求编排与 HTTP 生命周期耦合。

## 5. 本任务范围

- `internal/tools/coordinator`：非流式请求级模型—工具循环、协议解析、顺序执行和请求级预算。
- 定义与 Registry / Runtime 对接的抽象接口。
- 固化 Provider / Pipeline 缓冲接入决策和 Server 组合边界。
- Coordinator 单元测试及错误、预算、取消和 tool_choice 边界测试。

## 6. 本任务不实现

- SearXNG Adapter（已由 TOOL-04 完成）。
- 工具管理 API、管理台配置和完整调用方 ACL 产品流程（TOOL-07）。
- 工具调用审计持久化（TOOL-08）。
- 流式工具调用（TOOL-10）。
- 自动重试、并行工具执行、动态工具加载和通用 Agent 框架。
- 新增数据库表或拆分新的独立服务。

## 7. 验收条件

- Coordinator 不依赖 Provider、Server 或具体工具类型。
- 客户端 tools 定义不能覆盖服务端 Registry Definition 或扩大允许工具集合。
- 工具调用协议、参数 Schema、tool_call_id、tool_choice 和预算边界均有测试。
- 同轮工具按顺序执行；中间模型响应不会作为最终结果返回。
- Provider 错误响应与部分执行信息在 Coordinator 结果中保留。
- `go test ./...`、`go test -race`（含 `internal/tools/...`）、`go vet ./...` 和完整 Go CI 通过。

## 8. 最终验收记录

- 验收分支：`dev`。
- 验收代码 HEAD：`369e068f5aa8200bc387a78f11a46a87c14bb403`。
- Go CI：<https://github.com/Kasign/llm-gateway/actions/runs/44024743402>，结论 `success`。
- `go test ./...`：通过；`internal/tools/coordinator` 测试通过。
- `go test -race ./internal/provider ./internal/server ./internal/core ./internal/audit ./internal/live ./internal/tools/...`：通过；Coordinator Race 测试通过。
- `go vet ./...`：通过。
- Gateway 编译、Docker 镜像、配置/API 契约与前端构建：通过。
- Gateway 故障注入与恢复、MariaDB 审计积压、稳态负载与审计持久化：通过。
- TOOL-05 不包含 Provider Buffered Writer、Server 组装和 Pipeline 接入；这些工作明确移交 TOOL-06。

## 9. 验收结论

Coordinator 核心实现、协议约束、资源预算、边界测试和 Go CI 已满足 TOOL-05 验收条件，任务完成。下一步进入 TOOL-06，完成非流式 Chat Completions 请求级接入。
