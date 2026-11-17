# 当前任务：TOOL-06 非流式工具闭环接入

> 状态：进行中
> 更新日期：2026-11-17
> 仓库：Kasign/llm-gateway
> 分支：dev
> 开始基线：369e068f5aa8200bc387a78f11a46a87c14bb403
> 前置任务：[TOOL-05 请求级 Coordinator](任务历史/工具能力/TOOL-05-请求级Coordinator设计与实现.md)
> 任务状态权威来源：[整体任务规划](整体任务规划.md)

## 1. 任务目标

将已完成的 Coordinator 接入现有非流式 Chat Completions 请求路径，形成真正可用的模型—工具闭环。复用现有 Provider、鉴权、限流、请求改写和 Pipeline，不建立第二套推理链路。

## 2. 不可破坏的约束

- 只接入非流式 `/v1/chat/completions`；流式调用另归 TOOL-10。
- 一次客户端请求只执行一次 `Pipeline.Run` 和一次 `Finish`。
- 普通无工具请求保持原有路径和行为。
- 工具循环的所有模型轮次复用同一个 Provider 实例、同一个活动模型 ID 和同一请求 Context。
- 中间模型响应和工具结果不得写入客户端 ResponseWriter；只写出最终模型响应或原样保留的上游错误响应。
- Coordinator 不递归调用 Handler / Pipeline；循环只发生在同一次 Forward 调用内部。
- 工具定义来自服务端 Registry；客户端 tools 不能注册工具、覆盖定义或授予权限。
- 工具功能关闭、模型能力不支持或服务端策略不允许时，不得向上游发送服务端工具定义。
- 每次工具执行仍通过 Runtime，由 Runtime 重新授权；Coordinator 不从客户端字段推导权限。
- 不自动重试模型请求或工具执行，不并发执行同轮工具调用。
- 不新增数据库表；工具审计持久化由 TOOL-08 处理。

## 3. 接入前必须审查的技术点

1. **模型能力来源**：当前 Provider `Capabilities` 只有协议、Chat Completions、Streaming 和本地模型管理能力；工具调用是模型级能力，不能仅凭 OpenAI-compatible 协议或 Provider 类型推断。审查 ModelDescriptor、活动模型解析和现有配置，确定可审计、明确的服务端能力声明方式；不得默认所有模型都支持。
2. **响应写入契约**：审查 Provider 的 `Forward`、`InferenceCall.WriterWritten()`、`ClientWriter` 和本地 `llama.Client.Proxy`。确认缓冲 Writer 需要实现哪些接口，避免 Provider 误判响应已提交。
3. **缓冲适配器**：在 Server 内实现仅用于非流式工具模式的内部 ResponseWriter，捕获 Header、状态码和 Body，设置明确响应体上限；不能让中间轮次触达真实客户端 Writer。
4. **Provider 错误语义**：Provider 可能同时返回 `core.Result` 和 error。收到完整上游非 2xx 响应时保留状态、响应头和安全错误正文；连接失败、超时、取消等无完整响应的错误继续走现有错误映射。
5. **请求复用**：每轮只替换 Chat Completions Body；复用已解析的 Provider / ModelID，不重新解析模型、不重新执行 Mutator / Guard / WarmStart。
6. **回调副作用**：审查 `OnUpstreamDispatch`、`OnHeaders`、首字节计时和 Token Budget 标记在多轮调用中的语义，避免重复预留、错误覆盖或把多轮时间伪装成单轮时间。
7. **功能关闭语义**：客户端提供 tools / tool_choice 但服务端没有可用工具时，明确采用拒绝还是剥除策略；不能将客户端定义直接透传为 Gateway 可执行工具。
8. **响应提交**：最终响应只能提交一次；处理 Provider 已写入缓冲 Writer 后返回 error、空 Body、重复 WriteHeader 和 Body 超限等情况。

## 3.1 已核对的代码事实

- **模型能力尚无可靠的模型级声明。** `provider.Capabilities` 当前描述 Provider 协议、Chat Completions、Streaming 和本地模型管理；`proxyModelTarget` 在实际请求路由中只携带 `*model.Model`，而 `model.Model` 没有 Tool Calling 字段。远程 Provider 的 model mapping 也只有别名到上游 ID 的映射。不能把 Provider 支持 OpenAI-compatible Chat Completions 等同于当前模型支持工具调用。
- **Provider 缓冲可复用现有接口。** `InferenceCall.WriterWritten()` 通过可选的 `HeaderWritten() bool` 判断响应是否已提交；现有 `responseSink` 已实现该方法。工具模式的内部 Writer 必须实现 `http.ResponseWriter` 和 `HeaderWritten()`，并限制 Body 大小；非流式模式不需要 Flush。
- **两类现有 Provider 都能提供完整非流式 Body。** OpenAI-compatible Provider 的 `forwardJSON` 将响应写入传入 Writer，同时返回 `core.Result.Body`；非 2xx 时返回安全错误正文和 `ProviderError`。LocalHTTPProvider 的 `Proxy` 同样返回 Body、状态码和错误，并写入 Writer。
- **Provider 必须只解析一次。** 当前 `inferenceForwarder.Forward` 在单次 Forward 内 Resolve Provider。内部 ModelCaller 应闭包捕获这个 Provider 实例和 `st.Model.UpstreamID`，每轮只替换 Body；不能每轮重新 Resolve 或重新执行 Mutator / Guard / WarmStart。
- **请求级回调的现有副作用。** `OnUpstreamDispatch` 只将同一请求的 Token Budget reservation 标记为 Dispatched，重复调用是幂等的；`OnHeaders` / `OnFirstByte` 当前仅在流式路径写入阶段指标；`OnChunk` 仅用于流式；Outbound Header Probe 会覆盖同一请求的 upstream headers，最终只保留最后一轮。
- **多轮 usage 目前不会自动汇总。** Pipeline Finish 只从最终 `core.Result.Body` 解析非流式 Token usage；Coordinator 的 `RoundUsage` 尚未进入 `core.Result` 或 `callState`。若直接接入，审计和 Provider Token Budget 会低估多轮模型调用用量。TOOL-06 必须先定义可信的请求级 usage 汇总语义；不能把最后一轮 usage 当作总用量。
- **远程 Provider Token Budget 需要单独评估。** 当前 Guard 每个顶层请求只预占一次输入估算和一次输出上限，而 Coordinator 最多执行 4 次模型请求。远程 Provider 工具调用开放前，必须解决多轮预占与实际 usage 结算不匹配的问题；不能沿用单轮预算假设。
- **Pipeline 生命周期边界明确。** `core.Pipeline.Run` 只调用一次 Forward，并在结束时 Finish 一次；`responseSink.HeaderWritten()` 可用于防止 Handler 在已提交响应后再次写错误正文。

## 3.2 接入策略决策

- **Provider 协议能力与模型能力分开判断。** 先要求当前 Provider 声明支持 Chat Completions；再通过服务端显式模型白名单确认 Tool Calling。第一版不新增误导性的 Provider 级 `ToolCalling` 布尔值。
- **模型白名单使用稳定身份。** 配置项以 `provider_id + model_key` 匹配，不使用可变的上游 ModelID、Base URL 或 Provider 配置指纹。模型映射变化不应自动改变工具授权。
- **第一版使用服务端静态配置门控。** `tools.enabled` 默认 false；`allowed_models` 明确允许哪些模型；`allowed_tools` 明确允许哪些已注册工具。每请求根据解析后的 Provider / ModelKey 计算允许工具集合。TOOL-07 再扩展到 API Key 级授权和管理界面。
- **客户端不能创建工具。** 有服务端允许集合时，Coordinator 用 Registry 定义完全替换客户端 `tools`；没有允许集合但客户端明确提交 `tools`、`tool_choice`、旧版 `functions` 或 `function_call` 时，返回明确的不可用/未授权错误，不把客户端定义透传成 Gateway 可执行工具。第一版不支持旧版函数调用协议；普通无工具请求继续原样走现有路径。
- **只在非流式 Chat Completions 进入工具模式。** Provider 必须支持 Chat Completions，当前请求必须是 `/v1/chat/completions` 且 `stream=false`；其他路径和流式请求保持现有处理。
- **多轮 usage 与预算必须保守。** Coordinator 的所有轮次 usage 先保留原始证据；只有每轮 usage 都完整有效时才汇总为请求级总量。汇总结果供请求审计和 Provider Token Budget 结算使用，不改写最终模型响应中的原始 usage。当前版本对配置了每日 Token Budget 的远程 Provider 禁用工具模式：普通请求回退原路径，明确工具请求返回 503；待后续支持多轮预占/结算后再开放。

## 4. 目标接入结构

```text
HTTP Handler
    |
单次 core.Pipeline.Run
    |
inferenceForwarder.Forward
    |
解析一次 Provider + 活动 ModelID
    |
服务端功能开关 / 模型能力 / 请求授权判断
    |
Tool Coordinator
    |--------------------------|
内部 ModelCaller          Tool Runtime
    |                          |
同一 Provider             Registry + Policy
    |                          |
缓冲 ResponseWriter       Tool Modules
    |
只将最终响应写入真实客户端 Writer
    |
单次 Pipeline Finish
```

- 不支持工具调用或未授权时走普通 Provider Forward 路径，且不发送服务端工具定义。
- 工具模式开启时，由内部 ModelCaller 调用现有 Provider.Forward，并使用缓冲 Writer。
- Server 在每个请求开始时确定允许工具集合；Coordinator 只接收该集合。
- Provider / ModelID 在整个工具循环中固定；工具执行结果不触发模型切换。
- 最终响应由原 Forwarder 写回真实客户端，Pipeline 生命周期保持不变。

## 5. 实施顺序

1. 完成模型能力、Provider 缓冲 Writer、请求复用及多轮回调副作用审查。
2. 固化默认关闭的 Provider + ModelKey 白名单、客户端工具字段处理及远程 Token Budget 限制。
3. 完成 Server 工具模块装配、Buffered ModelCaller / ResponseWriter 和请求级 Coordinator 接入。
4. 验证 Provider / ModelID 复用、最终响应写回、上游非 2xx 保留、超时与 panic 诊断。
5. 补齐真实 Pipeline.Run 生命周期测试，验证单次 Forward / Finish 和中间响应隔离。
6. 运行全量 Go 测试、Race、Vet、构建及完整 Go CI；根据结果修复并 Review。
7. TOOL-06 验收后进入 TOOL-07；真实 SearXNG 网络验收仍留给 TOOL-09。

## 6. 必须覆盖的测试

- 普通无工具请求与当前行为一致，不进入 Coordinator。
- 工具功能关闭、模型能力不支持、策略不允许时，不向上游发送服务端 tools。
- 客户端伪造工具定义不能覆盖 Registry，也不能扩大允许工具集合。
- 工具调用后再次请求模型时 Provider 实例和 ModelID 不变。
- 同一请求只有一次 Pipeline.Run / Finish；中间轮次不写入真实 ResponseWriter。
- 最终响应 Header、状态码和 Body 正确写回，且只提交一次。
- 上游非 2xx 的完整响应与 Provider 错误同时存在时能保留；传输错误仍走原错误映射。
- 请求取消能够传递到 Provider 和 Runtime；工具执行失败不产生第二次 Pipeline 生命周期。
- 响应体上限、重复 WriteHeader、写入失败和 Provider panic 等边界可诊断。

## 7. 本任务不实现

- 工具管理 API、管理台配置和完整调用方 ACL 产品流程（TOOL-07）。
- 工具调用审计持久化（TOOL-08）。
- Gateway 容器到真实 SearXNG 的网络验收和整体回归（TOOL-09）。
- 流式工具调用（TOOL-10）。
- 新数据库表、独立工具服务、自动重试、并行工具调度或通用 Agent 框架。

## 8. 验收状态

- Pipeline.Run 生命周期集成测试已覆盖 `StageParsed`、`StageUpstreamStart`、`StageFinish`，并确认 Provider 工厂只解析一次、两轮调用使用同一活动 ModelID。
- 单元与集成测试、Race、静态检查、Gateway 构建、Docker 镜像、配置/API 契约/前端检查、Gateway 故障注入与恢复验证、稳定负载与审计持久化均通过。
- 最新代码测试提交：`10cf08161a5ed19a213be3745681ccfb444092ba`；Go CI Run `44030952014`：success。
- 文档归档提交：`936f1357102417a515a4e4bc62918fd9334d74c1`；对应 Go CI Run `44031172434`：success。
- Browser Smoke Run `44030952034` 在 Playwright Chromium 安装阶段失败，原因是 Runner 使用 `ubuntu26.04-x64`，Playwright 1.56.1 不支持该平台；桌面/窄屏浏览器断言未执行，移交 TOOL-09。
- Gateway 容器到真实 SearXNG 的部署网络连通性尚未验收，按计划移交 TOOL-09。

**验收结论：** TOOL-06 的非流式工具闭环、请求级生命周期、Provider / ModelID 复用、多轮 usage、错误处理和代码级测试均已完成；完整 Go CI 通过。Browser Smoke Runner 兼容性和真实 SearXNG 网络验收不属于本任务的代码闭环验收范围。


## 9. 实现记录与验收结果

### 实现内容

- 新增默认关闭的 `tools` 配置：`enabled`、`allowed_models`、`allowed_tools` 和 SearXNG 服务端配置。
- 模型授权身份固定为 `provider_id + model_key`；不使用上游 ModelID、Base URL 或配置指纹作为授权标识。
- Server 启动时统一装配 Tools Registry、SearXNG 工具模块和 fail-closed Runtime Authorizer。
- 新增内部 Buffered ModelCaller / ResponseWriter，单次请求复用已解析的 Provider 实例和活动 ModelID；中间模型轮次不会写入真实客户端 Writer。
- Coordinator 在同一次 `inferenceForwarder.Forward` 内执行多轮模型—工具交互；最终响应只提交一次。
- 多轮 usage 只有在每轮数据完整有效时才汇总；缺失或不完整时标记 unknown，不用最后一轮 usage 冒充总用量。
- 配置每日 Token Budget 的远程 Provider 暂不开放工具模式：无工具意图时保留普通请求路径；明确工具意图时安全拒绝。普通请求和多候选请求保持原路径。
- 上游完整非 2xx 响应保留状态、响应头和正文；工具超时映射 504；Provider panic 记录请求 ID、Provider ID、panic 类型和堆栈，不把 panic 值返回给客户端。
- 工具初始化失败会被记录，同时继续检查 Redis 和活动 Provider 等其他依赖，避免首个错误遮蔽其余诊断结果。

### 关键提交

- `ca9ee1a`：默认关闭的模型与工具白名单配置。
- `32ca711`：Server 装配 Registry 与 Runtime。
- `c2eee82`：非流式 Provider 缓冲调用适配。
- `543ccaa`：多轮 Token usage 汇总。
- `e2923a7`：单次 Forward 接入 Coordinator。
- `a30c0f1`：单次 Pipeline 生命周期集成测试。
- 最终代码验收提交：`10cf081`。

### 验收证据

- 分支：`dev`
- 验收提交：`10cf08161a5ed19a213be3745681ccfb444092ba`
- Go CI Run：`44030952014`
- Go CI 结论：**success**
- 通过项：单元与集成测试、Race、静态检查、Gateway 构建、Docker 镜像、配置/API 契约/前端检查、Gateway 故障注入与恢复验证、稳定负载与审计持久化测试。
- Pipeline 生命周期测试确认：一次 Pipeline Run 内完成两轮模型调用，`StageUpstreamStart` 和 `StageFinish` 均只发生一次，客户端只收到最终模型响应。
- Browser Smoke Run `44030373708` 未通过：失败发生在安装 Playwright Chromium 阶段，Runner 的 `ubuntu26.04-x64` 不受 Playwright 1.56.1 支持；浏览器交互断言被跳过。该环境问题转 TOOL-09 处理，不作为 TOOL-06 代码通过证据。
- Gateway 容器到真实 SearXNG 的部署网络连通性尚未验收，按计划转 TOOL-09。

### 验收结论

TOOL-06 的非流式工具闭环、请求级生命周期、Provider 复用、错误响应处理和代码级测试已完成，Go CI 全部通过。Browser Smoke Runner 兼容性和真实 SearXNG 网络验收保留在 TOOL-09，不阻塞本任务归档。
