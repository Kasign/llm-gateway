# PERF-01：现有字段与 timings 盘点

> 状态：已完成（代码审查与字段矩阵）
> 更新日期：2026-11-18
> 仓库：Kasign/llm-gateway
> 分支：dev
> 验收基线：dead2a2b9806dc5e0dc958d51cfa4ad565efdbcd

## 1. 审查范围

- `internal/server/request_analysis.go`、`request_analysis_persist.go`、请求收尾与历史详情重建。
- `internal/core.State` Pipeline 时间锚点。
- `internal/logutil/tokens.go`、SSE 拼包与 TokenHarvest。
- OpenAI-compatible Provider 并发许可器、FIFO 等待队列。
- 本地 LocalHTTPProvider 与 llama.cpp/Ollama Runtime Snapshot。
- Benchmark 指标复用边界。

## 2. 字段矩阵与审查结论

| 字段 | 来源 | 类型/状态 | 结论 |
|---|---|---|---|
| total_ms | Finish - Started | 实测 | 与 RequestLog.latency_ms 重复，可继续复用 |
| prep_ms | PreparedAt - Started | 实测/条件性 | 覆盖解析、Resolver、Mutator；未到锚点时当前写 0 |
| model_load_ms | ReadyAt - PreparedAt | 实测/条件性 | 覆盖 Guard 与模型就绪等待，不是纯模型加载 |
| upstream_ms | Finish - UpstreamStart | 实测/条件性 | 包含 Forward 内 Provider 队列、HTTP 往返及工具 Coordinator |
| upstream_headers / first_byte | Provider 回调 | 实测/条件性 | 当前主要在流式路径回填；非流式路径缺少相同观测 |
| TTFT / TTFT content | Gateway 时间线、SSE 事件 | 实测/条件性 | 普通非流式请求通常没有可用 TTFT |
| prefill_until_first | TTFT 减 Gateway 前置阶段 | 派生 | 仍含队列、网络、Prompt Eval，不能称为纯 Prefill |
| thinking / content_generation | SSE reasoning/content 时间戳 | 派生 | 不是后端原生 profiler 指标；非流式 content_generation 复用 upstream_ms |
| output_tokens_per_sec | llama predicted_per_second 或 Token/窗口 | 混合 | 当前来源标签混合 Token 来源与速度来源，存在误标风险 |
| prompt_ms / predicted_ms / prompt_per_second | llama.cpp timings | 已解析、未持久化 | TokenHarvest 未保留；解析器有效性判断只看 Token 数/Decode 速度 |
| provider_queue_wait_ms | 暂无单请求采集 | 缺失 | 只有进程级并发、队列深度、拒绝与超时指标 |
| Runtime Snapshot | 模型目录/加载列表/RAM/VRAM | 进程级快照 | 缓存快照，不是单请求测量 |

## 3. 已确认的数据口径问题

1. `RequestThroughput.Source` 默认写成 `upstream_usage`，即使没有 Token 来源证据也会保留该值。
2. usage 与 llama timings 同时存在时，Token 来源与速度来源不同，但只用一个 Source 表达。
3. Token 来源为 `llama_timings`、但没有 Decode 速度时，速度可能改用 Token 数除以窗口计算，摘要仍可能标记为 llama timings 实测。
4. llama Decode 速度缺少生成窗口时，当前可能把 upstream_ms 作为窗口；该窗口包含队列/网络，不等于 Decode 时间。
5. timings 解析器可能丢弃仅包含 prompt_ms、predicted_ms 或 prompt_per_second 的对象。
6. 历史详情重建没有阶段耗时来源时，阶段字段默认 0，无法区分真实 0 与未知。
7. OpenAI-compatible Provider 的队列等待在单请求层没有计时，合并进 upstream_ms；本地后端内部排队不可见。

## 4. PERF 后续建议顺序

- **PERF-02 指标来源与 timings 完整性**：分离 Token 来源与速度来源；保留 Prompt Eval/Decode 原始 timings；修复缺失来源与窗口口径；维持既有 JSON 字段兼容。
- **PERF-03 Provider 单请求队列等待**：在并发许可器 Acquire/Release 边界记录单请求等待，覆盖排队、直通、拒绝、超时、取消；不得改变队列顺序与取消时序。
- **PERF-04 阶段耗时测量状态**：明确未发生、未测量、测量失败与真实 0；兼容历史 analysis_json 和 RequestLog 回退。
- **PERF-05 非流式上游首包时序**：补齐 headers/first-byte 采集，确保不缓冲响应、不改变非流式响应语义。
- **PERF-06 结果详情与可比性展示**：仅展示有证据的字段、来源和缺失状态；不在前端推导服务端指标。

## 5. 不可破坏约束

- 普通 `/v1/chat/completions` 协议、认证、Pipeline 顺序、Provider 选择、流式响应、错误处理与取消时序不变。
- 不把 Gateway 准备、模型就绪、Provider 排队、网络、Prompt Eval、Decode 混成一个指标。
- 新字段必须说明来源、起止点、单位、适用范围和缺失状态。
- 优先复用既有请求日志、流事件与 Benchmark 数据；不重复存储 Prompt、Answer、Reasoning 或工具正文。
- 只在本地自动化验证；真实部署回归按用户决定后续统一进行。

## 6. 验证

- Go CI run **44234272317**：成功。
- 该任务仅进行代码审查与文档整理，没有改动推理主链路。
