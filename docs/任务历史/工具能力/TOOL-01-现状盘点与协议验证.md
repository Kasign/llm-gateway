# TOOL-01 现状盘点与协议验证

> 归档状态：已完成
> 仓库：Kasign/llm-gateway
> 工作分支：dev
> 开始基线：a8ebb08eef2279fa8b283780eb41ba60624e18f0
> 完成证据：用户于 2026-11-17 提供的本地模型与 SearXNG 真实运行输出
> 任务状态权威来源：[整体任务规划](../../整体任务规划.md)

## 1. 任务目标

在不修改业务代码的前提下，确认当前 Provider、Chat Completions 和 Pipeline 对工具调用的真实支持情况，并核实已部署 SearXNG 的接口与真实响应。最终给出第一版工具调用的可行性结论和后续实现边界。

## 2. 范围与非目标

本任务只做：
- Provider 能力声明、请求构造和响应转发审计。
- 本地 llama.cpp / Ollama Provider 的实际配置与协议能力审计。
- Chat Completions 的 tools、tool_choice、tool_calls 透传和响应样本验证。
- Pipeline / Server 请求生命周期与审计边界审计。
- SearXNG 实际 JSON API、访问地址、参数和响应字段验证。
- 输出可行性结论及 TOOL-02～TOOL-06 的实现前置约束。

本任务不做：
- 不新增 Tool Registry、Runtime、SearXNG Adapter 或管理 API。
- 不修改现有推理主流程。
- 不进行通用 Agent 框架建设。
- 不将工具能力扩展到 Responses、Anthropic 或流式场景。

## 3. 代码审计结论

### 3.1 Provider 能力声明

检查 internal/provider/provider.go。

当前 Capabilities 只有 protocol、chat_completions、streaming、local_model_management，没有 Tool Calling / Function Calling 能力字段。因此当前 Gateway 无法通过 Provider 能力声明判断活动模型是否支持工具调用。

### 3.2 本地 Provider

检查 internal/provider/local_http.go。

- llama.cpp 和 Ollama 当前通过 LocalHTTPProvider 注册。
- 构造时统一声明 protocol=openai-compatible、支持 Chat Completions 和 Streaming。
- Forward 将请求体交给本地 HTTP Client 的 Proxy 或 StreamProxy。
- Provider 层没有解析模型返回的 tool_calls，没有工具执行或多轮对话循环。

传输层可以把请求交给本地后端，但不能据此认定所有本地模型实际支持稳定工具调用。

### 3.3 远程 OpenAI-compatible Provider

检查 internal/provider/openai_compatible.go。

- Forward 通过 rewriteRemoteModel 改写服务端选定的模型 ID，然后发起 HTTP 请求。
- 请求体按 JSON Object 解析并改写 model 字段；Provider 不负责理解工具调用语义。
- JSON 响应由 forwardJSON 处理，流式响应由 forwardStream 处理。
- 当前实现属于协议转发，不包含 Gateway 内部工具调用循环。

当前实现没有 Gateway 工具执行能力；缺少专项测试不代表上游 Provider 不支持工具调用。

### 3.4 请求体与响应透传

检查 internal/server/inference_apply.go、internal/inferenceparams/merge.go、internal/provider/openai_compatible.go、internal/llama/client.go。

已确认：
- 本地后端在需要合并推理参数时，MergeChatBody 将 JSON 解码为通用对象，合并采样/思考参数后重新编码；tools、tool_choice 等顶层字段会保留。
- 远程 Provider 的 rewriteRemoteModel 只改写服务端选定的 model 字段。
- 远程非流式成功响应由 forwardJSON 读取并原样写回；远程流式响应由 forwardStream 透传 SSE 字节。
- 本地非流式响应由 llama.Client.Proxy 原样写回；本地流式响应由 StreamProxy 透传。
- Gateway 不会解析 finish_reason=tool_calls、提取调用参数、执行工具或把工具结果作为下一轮消息回传。

结论：当前属于“模型协议透传”，不是“Gateway 工具调用支持”。

### 3.5 Pipeline 与请求生命周期

检查 internal/core/pipeline.go、internal/server/pipeline_http.go、internal/server/pipeline_forward.go、internal/server/proxy_v1_chat_flow.go。

已确认：
- 一次 Pipeline.Run 执行 Parse、Resolve、Mutate、Guard、WarmStart、Forward，并通过 defer 只发出一次 Finish 事件。
- serveInference 对一次客户端请求调用一次 s.pipe.Run。
- inferenceForwarder.Forward 解析活动 Provider 并调用一次 Provider.Forward。
- 现有 Provider 在单次调用中直接将上游响应写回客户端；流式响应可能已经提交响应头和正文。

因此，不能在 Handler 中递归调用完整 Pipeline 实现工具循环。后续必须由请求级 Coordinator 管理多轮上游调用，并保持单一顶层请求记录和 Finish 生命周期。

### 3.6 本地请求清理与 tools 保留验证

检查 internal/server/openai_sanitize.go、internal/server/inference_apply.go、internal/inferenceparams/merge.go、internal/server/responses_passthrough_test.go。

已确认：
- 本地后端的 Chat Completions / Responses 请求会经过 sanitizeForLlama；Chat 清理逻辑只重排 system/developer messages，并处理流式默认值，不会删除顶层 tools 字段。
- TestSanitizeKeepsTools 覆盖 Chat Completions 和 Responses 两种路径，检查清理后仍保留不同类型的 tools 项。
- MergeChatBody 未主动删除 tools 或 tool_choice。
- 当前测试证明的是请求体字段保留，不是工具调用解析和执行。

## 4. Coordinator 架构边界

1. 一个客户端请求只进入一次 Pipeline.Run，只生成一条顶层请求记录，并且只触发一次 Finish。
2. Coordinator 位于 Forward 编排层；不得在 Handler 里重新调用 Pipeline。
3. 第一版只接受 stream=false 的 Chat Completions 工具模式；流式工具模式须在上游响应写出前明确拒绝。
4. 每轮 Provider 响应先写入内部受限缓冲区；检查 HTTP 状态、响应大小、JSON 结构和 finish_reason，只有最终响应写给客户端。
5. 所有模型轮次和工具执行共享客户端取消与 Gateway 超时 Context；限制模型轮数、工具数、单工具超时、总执行时间和累计响应字节。
6. 只允许 Registry 中已注册且本次请求授权的工具；校验工具名、参数 JSON Schema、参数大小和调用数量。
7. 识别标准 choices[].finish_reason=tool_calls 与 message.tool_calls，校验 tool_call_id，追加 assistant tool-call 消息及 tool 结果后再请求下一轮模型。
8. 最终只返回最终轮兼容 Chat Completions 响应；内部明确累计各轮 Token 口径，不伪造上游未提供的 usage。
9. 工具错误和模型轮次超限关联同一 request_id；工具调用作为子事件，不创建独立顶层推理请求。
10. 工具默认关闭；服务端配置/管理端授权决定可用工具，客户端传入 tools 不能自动授权。SearXNG 只接受服务端固定 Base URL。

第一版多工具调用建议顺序执行，避免并发放大副作用和资源占用；具体限制由后续 Runtime 任务落地。

## 5. 真实环境验证

### 5.1 本地模型 Tool Calling

用户提供的实际命令通过本地 llama.cpp OpenAI Chat Completions 端点调用模型。响应中的模型为 Qwen3.5-4B-IQ4_NL。

样本关键字段：
- finish_reason = "tool_calls"
- message.tool_calls 存在
- 工具名称：search_web
- arguments = {"query":"Go 语言最新稳定版本"}
- tool_call_id 存在
- usage 与 llama.cpp timings 均有返回

结论：该模型、当前 llama.cpp 部署和本次工具定义组合，能够生成标准 Chat Completions Tool Calling 响应，arguments 是合法 JSON 字符串。

限制：
- 这是单次成功样本，不代表复杂参数、多工具调用、连续多轮或其他模型均稳定。
- 本次样本模型是 Qwen3.5-4B-IQ4_NL，不应沿用此前计划中的 Qwen3.5-9B-Q4_K_M 作为样本型号。
- 尚未验证工具结果回填后的第二轮模型响应；该项属于后续闭环验收。

### 5.2 SearXNG JSON API

用户提供的真实请求：
GET http://127.0.0.1:8080/search?q=Go%20release&format=json

实际返回 JSON，顶层包含 query、results、answers、corrections、infoboxes、suggestions、unresponsive_engines。结果项包含 title、url、content、engine、score、publishedDate 等字段。

结果中包含 Go 官方发布历史页面及摘要；同时部分搜索引擎出现验证码或无响应记录。

结论：
- 当前 SearXNG 实例已启用 JSON 搜索接口。
- 搜索路径为 /search，查询参数至少确认 q 和 format=json。
- 返回结构足以支撑第一版搜索 Adapter 的字段归一化设计。

限制：
- 用户命令中的 127.0.0.1:8080 仅证明执行该命令的主机环境可访问；尚未证明 Gateway 容器能通过同一地址访问 SearXNG。部署网络地址和容器连通性在 TOOL-04 / TOOL-09 处理。
- 本次输出未包含 SearXNG 版本号；第一版 Adapter 应以已观察到的 JSON 字段为准，并对缺失字段做兼容处理。
- 搜索引擎验证码/无响应属于搜索质量和依赖诊断，不等同于 JSON API 不可用。

## 6. 最终结论与验收

| 验收项 | 结论 | 证据 |
|---|---|---|
| Provider 能力与请求/响应边界 | 通过 | 代码审计 |
| tools 请求字段保留 | 通过 | TestSanitizeKeepsTools 及参数合并路径审计 |
| Pipeline 单一生命周期约束 | 通过 | Pipeline / Handler / Forward 代码审计 |
| 本地模型标准 Tool Calling 样本 | 通过（单次样本） | Qwen3.5-4B-IQ4_NL 真实响应 |
| SearXNG JSON API | 通过（主机侧） | /search?q=...&format=json 真实响应 |
| Gateway 容器访问 SearXNG | 未验证 | 后续 Adapter 部署验收 |
| 多轮工具闭环 | 未实现、非本任务范围 | TOOL-05 / TOOL-06 |

TOOL-01 的目标是可行性与协议边界确认，不要求在本任务实现工具执行闭环。以上证据满足本任务验收条件；未验证事项已明确移交后续任务，不作为隐含的已通过结论。

## 7. 后续实现约束

- 先完成 Tool Definition / Registry 的类型、Schema 校验与注册规则，再实现 Runtime 和 Adapter。
- JSON Schema 第一版按产品技术方案约定的受限子集实现；不引入远程引用解析。
- 工具名称、描述和参数 Schema 由服务端注册定义，不接受客户端借 tools 字段新增执行能力。
- 当前模型样本证明可进入标准 Tool Calling 路径，但 Provider/模型能力不能仅由 openai-compatible 协议名推断。
- 搜索结果是外部不可信数据，不能改变系统策略或工具权限。
- SearXNG Base URL 由服务端配置固定；工具参数只接收受控搜索条件。
- 继续保持普通无工具请求行为不变。

## 8. 过程记录

- 代码审计和 Coordinator 边界记录在此前 docs/当前任务.md。
- 最新审计文档提交：79169656f203028dd8acc2f48456099e5928a5f3。
- 该提交的 GitHub Actions workflow 查询未返回运行记录，Combined Status 也未返回状态；不得标记该提交 CI 通过。
- 本任务没有修改业务代码。
- 用户真实环境输出由本归档记录，未记录任何 API Key 或其他凭据。
