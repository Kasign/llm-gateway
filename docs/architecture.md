# 技术架构

LLM Gateway 是个人使用的模型接入、管理与能力扩展平台：统一接入本地模型和远程模型，并逐步提供受控工具调用、推理性能分析和模型来源管理。它不是通用 Chat UI，也不以让慢速本地模型承载 Codex / Claude Code 等长连接 Coding Agent 为主要目标。

产品能力扩展的统一规划见 [产品能力技术方案](产品能力技术方案.md)。文档导航与权威范围见 [文档目录](README.md)。

进程入口是 `cmd/gateway/main.go`：读配置、迁移 MariaDB、构造 `server.Server`、监听 HTTP。Redis 或上游暂时不可用时进程仍启动。

## 依赖各自做什么

| 组件 | 职责 |
|---|---|
| Gateway | 鉴权、限流、按既定产品语义解析活动模型、改写出站 body、转发、调用记录、管理台 |
| llama.cpp / Ollama | 真正推理。工具类型是否支持由上游决定 |
| MariaDB | 模型、API Key、请求日志、可选的请求/响应原文 |
| Redis | API Key 的 RPM 限流、下载进度等瞬态状态 |

管理台全局状态见 [admin-state.md](admin-state.md)。

## HTTP 面

`/v1/models` 由 Gateway 读数据库组装，不访问上游。

`POST /v1/*`（`/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/completions` 以及其他 POST）进入同一条推理主流程。登录、下载、模型管理、Benchmark 的 HTTP 接口不进入这条流程。Benchmark 在进程内复用同一条 `Pipeline.Run`。

门外（`serveInference`，不在 `Pipeline.Run` 里）按顺序做：只接受 POST、API Key 鉴权、按 Key 限流、读 body（最多 64MB）、以客户端连接为父创建 context，再套上 `request_timeout_seconds`，并登记取消函数。门外拒绝的请求仍写一条调用记录，状态为错误，不会到达上游。

一次推理在以下情况结束，上游请求一并取消：

| 原因 | 调用记录 |
|---|---|
| 客户端断开连接 | `cancelled`，`error_message` 为「客户端连接已断开」 |
| 保活注释写不出去 | `cancelled`，`error_message` 以「保活写出失败」开头，并带写出错误 |
| 上游内容写回客户端失败 | `cancelled`，`error_message` 以「写回客户端失败」开头，并带写出错误 |
| 管理端 `POST /api/requests/{id}/cancel` | `cancelled`，`error_message` 为「管理端结束这次调用」。已经结束的请求返回 409 |
| 达到 `request_timeout_seconds` | `error` / `gateway_timeout`，`error_message` 为「网关请求超时」 |

先发生的原因保留，后面的取消不再覆盖。进程日志里的 `inference finished` 带同一条 `error`。流式保活用来避免长时间 prefill 被客户端空闲超时关掉；保活注释不属于模型回答，也不计入 TTFT。

模型下载、Ollama pull、Benchmark 使用各自的后台 context。关掉管理页不会停止它们，由对应的取消入口或下载超时结束。

## 推理主流程

顺序写死在 `internal/core/pipeline.go` 的 `Pipeline.Run`，不随鉴权、日志或某一次业务改写改变：

```
Parse → Resolve → Stamp → Mutate → Guard → WarmStart → Forward → Finish
```

`core` 不引用鉴权、数据库、模型库或 `net/http`。这些能力由 `server` 注入的回调提供。组合根是 `Server.newInferencePipeline`：新增改写、检查或日志只改这里的注册，不改 `Pipeline.Run`。

| 阶段 | 固定行为 |
|---|---|
| Parse | 复制 body，记下客户端 `model`、是否流式、原始路径 |
| Resolve | 普通 HTTP 推理忽略客户端 JSON 里的 `model`，使用管理台当前活动模型；`ServiceModel` 仅供 Benchmark 等进程内调用 |
| Stamp | 把选定的上游模型 ID 写回 body 的 `model`。之后改写和上游看到的是同一个模型 |
| Mutate | 按注册顺序改路径和 body |
| Guard | 出网前检查。先全局 Guard，再本次请求附加的 Guard |
| WarmStart | 配置打开且本次是流式时，先写 SSE 注释并 flush，再按间隔重复，直到本次 Run 结束。保活不进入回答，也不计入 TTFT |
| Forward | 唯一出网。流式时每块上游字节交给观察者 |
| Finish | 无论成功、失败或 panic 都只发一次。写请求日志和耗时分析。成功后才 Touch API Key、标记模型已使用。观察者出错不影响响应 |

## 回调槽位

`Pipeline` 的槽位保持注册。业务放在回调里，不写进 `Pipeline.Run`。

当前注册（`newInferencePipeline`）：

| 槽位 | 回调 | 现在做什么 |
|---|---|---|
| Resolve | `resolveInference` | 选定活动上游模型 |
| Mutate | `mutateSanitize` | 按路径整理 body；`tools` 不改 |
| Mutate | `mutateInferenceParams` | 按管理台策略合并推理参数 |
| Mutate | `mutateResponsesBridge` | 保持路径与 body 的既有协议语义 |
| Guard | `guardModelReady` | 检查模型就绪状态 |
| Guard | `allowModelsGuard` | 检查调用方允许使用的模型 |
| WarmStart | `warmStart` | 流式保活注释 |
| Forward | `inferenceForwarder` | 唯一出网，上游响应原样写回 |
| Observe | `inferenceObserver` | 调用记录、实时看板、流式事件、可选原文；只读事件，不改变响应 |

## 新能力接入边界

工具执行、本地模型性能分析和免费模型聚合的目标架构、任务拆解与验收条件见 [产品能力技术方案](产品能力技术方案.md)。这些是规划能力，并不代表当前代码已经实现。

特别注意：
- 工具循环不能简单递归调用完整的 `Pipeline.Run`，否则可能为一次客户端请求生成多条最终审计记录。必须先设计请求级 Coordinator 和单一请求生命周期。
- 工具 Runtime 不应放进 `internal/core.Pipeline.Run`；由组合根注入并在明确策略下调用。
- 性能分析优先扩展现有 `request_analysis.go`、流事件和 llama.cpp timings，不重复建设指标系统。
- 免费模型聚合复用现有 Provider Registry 和加密凭据管理，不在推理热路径抓取厂商免费政策。
- 新功能默认关闭或只读，不改变当前活动模型选择语义。
