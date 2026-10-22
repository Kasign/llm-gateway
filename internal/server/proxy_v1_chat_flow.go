package server

/*
POST /v1/chat/completions（以及同前缀的 /v1/responses 等）

主流程在 internal/core.Pipeline.Run，顺序固定，不随鉴权、日志或保活改变：

  Parse → Resolve → Stamp → Mutate → Guard → WarmStart → Forward → Finish

门外（serveInference，不在 Run 里）：
  - 只接受 POST
  - API Key 鉴权（过期是 key_expired，无效是 invalid_api_key）
  - 按 Key 限流
  - 读 body（最多 64MB）
  - 用客户端连接创建 context，再套上网关超时，并按请求 ID 登记取消函数

Run 里各槽位的现实现：
  - Resolve：忽略客户端 JSON 的 model。有 ServiceModel（仅 benchmark 等进程内调用）才用它，否则用管理台当前推理模型。
  - Stamp：把选定的上游 ID 写回 body。
  - Mutate：sanitize、推理参数。/v1/responses 的路径和 tools 保持原样，交给上游。
  - Guard：auto_load 时等待模型就绪。chat 失败返回 503；/v1/responses 最多等 20 秒，失败仍转发。
    /v1 另附 allowed_models 检查，benchmark 不附。
  - WarmStart：配置 warm_start 打开且本次是流式时，先写一行 SSE 注释并 flush，再按间隔重复，直到本次 Run 结束。每一次写出各记一条流式事件。不进入回答，也不计入 TTFT。写出失败则取消这次推理。
  - Forward：唯一出网。流式每块只把上游字节交给观察者，并原样写回客户端。写回失败则取消这次推理。
  - Finish：请求日志、耗时分析、成功后才 Touch / MarkUsed。观察者出错不影响响应。

结束条件：
  客户端断开、保活或写回失败、管理端 POST /api/requests/{id}/cancel、网关请求超时。
  前三类日志状态为 cancelled，超时为 gateway_timeout。error_message 写具体原因。
  已经结束的请求返回 409。

登录、下载、模型管理和 /v1/models 不进入 Run。
*/
