# 技术架构

Gateway 只做本地 Coding Agent 的入口。Cursor / Codex / Claude Code 访问 Gateway；Gateway 再调用已经在跑的 llama.cpp 或 Ollama。公网只开放 OpenResty。llama.cpp、Redis、MariaDB 使用现有容器，不由本仓库的 Compose 重建。

进程入口是 `cmd/gateway/main.go`：读配置、迁移 MariaDB、构造 `server.Server`、监听 HTTP。Redis 或上游暂时不可用时进程仍启动。

## 依赖各自做什么

| 组件 | 职责 |
|---|---|
| Gateway | 鉴权、限流、选模型、改写出站 body、转发、调用记录、管理台 |
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

先发生的原因保留，后面的取消不再覆盖。进程日志里的 `inference finished` 带同一条 `error`。流式保活用来避免长时间 prefill 被客户端空闲超时关掉；`warm_start` 关闭时，这种空闲断开同样会结束本次推理。

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
| Resolve | 忽略客户端 JSON 里的 `model`。有 `ServiceModel`（仅 Benchmark 等进程内调用）才用它，否则用管理台当前推理模型 |
| Stamp | 把选定的上游模型 ID 写回 body 的 `model`。之后改写和上游看到的是同一个模型 |
| Mutate | 按注册顺序改路径和 body |
| Guard | 出网前检查。先全局 Guard，再本次请求附加的 Guard |
| WarmStart | 配置打开且本次是流式时，先写 SSE 注释并 flush，再按间隔重复，直到本次 Run 结束。保活不进入回答，也不计入 TTFT |
| Forward | 唯一出网。流式时每块上游字节交给观察者 |
| Finish | 无论成功、失败或 panic 都只发一次。写请求日志和耗时分析。成功后才 Touch API Key、标记模型已使用。观察者出错不影响响应 |

## 回调槽位

`Pipeline` 的槽位保持注册。`Run` 按固定顺序调用它们。业务放在回调里，不写进 `Pipeline.Run`。

当前注册（`newInferencePipeline`）：

| 槽位 | 回调 | 现在做什么 |
|---|---|---|
| Resolve | `resolveInference` | 选定上游模型 |
| Mutate | `mutateSanitize` | 按路径整理 body。system / developer 合并到首位；流式时注入 `stream_options.include_usage`。`tools` 不改 |
| Mutate | `mutateInferenceParams` | 按管理台策略合并推理参数。作用于 chat、messages、responses |
| Mutate | `mutateResponsesBridge` | 留在列表末尾。不改路径，不改 body |
| Guard | `guardModelReady` | `auto_load_on_proxy` 打开时等待模型就绪。chat 加载失败返回 503。`/v1/responses` 最多等 20 秒，失败仍转发 |
| Guard | `allowModelsGuard` | 仅 `/v1` 门外按 API Key 附加。比对的是服务选定的模型 |
| WarmStart | `warmStart` | 流式保活注释。写出失败则取消这次推理 |
| Forward | `inferenceForwarder` | POST 到上游。上游字节原样写回客户端。写回失败则取消这次推理 |
| Observe | `inferenceObserver` | 调用记录、实时看板、流式事件、可选原文。只读事件，不改变响应 |

Mutate 的注册顺序：先按客户端原始路径做 sanitize 和推理参数，最后 `mutateResponsesBridge` 保持路径与 body。`request_body` 在 Prepared 之后保存，内容是全部 Mutate 完成、真正发给上游的 JSON。调用记录上的 endpoint 仍是客户端路径。

## Responses 透传

上游是 1Panel 中的 `ghcr.io/ggml-org/llama.cpp:server`，当前运行版本为 **b11580**。llama.cpp 自己提供 `POST /v1/responses`，内部再译成 chat completions：保留 `type: "function"` 的 tool，跳过它不执行的类型（如 `web_search`、`namespace`）。Codex 的 MCP 工具包在 `namespace` 里，跳过是 llama.cpp 的行为。

Gateway 对 `/v1/responses` 的处理：

- `/v1/responses` 的路径保持不变，原样 POST 给上游。
- `tools` 原样保留。function、`namespace`、MCP、`web_search` 以及其他 Gateway 不认识的类型都不删、不改、不补。
- 上游 SSE 直接写回客户端，其中的 `function_call` 也原样回去。
- 调用记录写「`/v1/responses` 原样转发，tools 不改写」。`request_body` 是转发给上游的 JSON。

`mutateSanitize` 负责 system / developer 合并，以及流式 `stream_options.include_usage`。Stamp 写入上游模型 ID。`mutateInferenceParams` 合并管理台推理参数。`guardModelReady` 对 `/v1/responses` 最多等 20 秒，超时仍转发；这只影响等待，不改 body，也不判断 tool 类型。

Codex 的 function tool 和对应的 `function_call` 会到达 llama.cpp。`namespace` / MCP 是否可见由上游决定。
