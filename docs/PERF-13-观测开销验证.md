# PERF-13 观测开销验证方案

> 状态：预研完成，等待 PERF-11 验收后启动
> 日期：2026-11-23
> 分支：dev
> 状态权威来源：`docs/整体任务规划.md`

## 1. 目标

量化 Gateway 现有请求观测链路对推理请求的额外开销，重点回答：

- 对流式请求 TTFT 与完整请求延迟分布（P50/P95）的影响有多大；
- 对 Gateway CPU、分配次数、堆内存和 RSS 的影响有多大；
- 开销主要来自 SSE 解析、事件记录、Live Session 更新、分析 DTO/JSON 编码，还是 Runtime Snapshot 读取；
- 是否存在随输出长度、并发数或事件数量线性/非线性增长的情况。

本任务不是增加新观测能力，也不是优化指标展示；只有实测证明存在明显成本，才进入针对性优化。

## 2. 当前代码事实

1. 流式 chunk 经 `inferenceObserver.onChunk` 处理，再进入请求 trace；`requestTrace.recordStreamChunk` 解析 SSE 行并提取 TTFT、首 reasoning/content、finish、usage 等事件。
2. `appendStreamEvent` 对事件数限制为每请求 2000 条；预览和原始 SSE 默认关闭，并有每请求总字节预算。达到事件上限后仍会执行 chunk 解析和 Observer 更新，但不再追加事件。
3. `BenchmarkRequestTraceRecordStreamChunk` 已测 SSE 解析/事件元数据路径；`BenchmarkInferenceObserverOnChunkParallel` 已测事件预算耗尽后的并发稳态 Observer 路径。
4. `buildRequestAnalysis` 已有 DTO 转换基准；当前缺少将完整 Observer 开销拆成“无观察基线 / Trace / Observer / 分析序列化”同机对照的完整基准组。
5. Runtime Snapshot 在请求收尾阶段读取最近样本并附加到 RequestAnalysis，不在每个 chunk 上主动探测上游；数据库写入通过现有 Audit 持久化链路完成。
6. 当前普通请求观测没有一个可用于生产 A/B 的总开关。因此不能直接拿两次不同日期/负载的真实模型请求，声称差值就是观测开销；也不能为了测试随意增加用户可关闭核心审计的开关。

## 3. 测量分层

### 3.1 同版本微基准（先做）

在同一 Gateway 代码版本、同一机器和 Go 运行时上，使用固定 SSE chunk 与固定事件序列，建立以下对照：

| 组别 | 路径 | 目的 |
|---|---|---|
| A | 仅消费 chunk，不解析/记录 | 测量基准开销 |
| B | requestTrace.recordStreamChunk | 测量 SSE 解析与 Trace 记录增量 |
| C | inferenceObserver.onChunk，事件预算未耗尽 | 测量完整 chunk 观察路径 |
| D | inferenceObserver.onChunk，事件预算已耗尽 | 测量长流稳态路径 |
| E | buildRequestAnalysis + encodeRequestAnalysis | 测量请求结束分析构建与 JSON 编码 |
| F | Runtime Snapshot 读取/映射 | 测量请求收尾快照开销 |

统一记录 `ns/op`、`B/op`、`allocs/op`；串行和并行路径分开，避免把并发竞争误当成单请求成本。重复运行并保存 Go 版本、OS/架构、CPU、commit SHA、命令行、原始输出。该层可以量化 Gateway 代码路径成本，但不能直接等同于真实模型 TTFT 增量。

### 3.2 内存与长流边界

- 对事件数 0、达到上限前、达到上限后分别测量。
- 对默认配置与显式开启 preview/raw 的配置分开测量；默认配置是主要验收对象。
- 记录每请求事件数、保留字节、对象分配和堆内存变化。
- 用固定输出长度阶梯和并发阶梯观察增长趋势；不能仅以单个平均值判断无泄漏。
- RSS 必须区分 Go heap、runtime reserved memory 与进程 RSS；短时 GC 前后 RSS 不可直接比较。

### 3.3 真实模型端到端验证（后做）

在用户真实 NUC 部署上固定模型、模型参数、Gateway 配置、Prompt/输出 Token 目标、缓存状态和并发条件，采集多轮 TTFT、总延迟与 Gateway RSS。必须先完成预热，再分批交错运行，避免温度、缓存、后台任务和时间顺序偏差。

当前代码没有生产可用的“关闭请求观测”对照开关，因此端到端样本只能证明部署运行时的延迟/RSS 分布，不能单独证明观测造成的差值。若微基准显示成本值得继续追因，再单独设计仅用于受控验收的可逆对照方式；未经评估不得增加生产配置开关，也不得关闭鉴权、错误审计或请求关联能力。

## 4. 不可比条件

以下任一变化必须分组，不能合并统计：

- 模型文件、量化、后端、GPU offload、线程数、上下文长度；
- llama.cpp 版本/build、Gateway commit、Go 版本；
- Prompt Token、目标输出 Token、采样参数；
- 冷/部分/完整 KV cache；
- 并发数、请求到达节奏、是否有其他后台任务；
- preview/raw 保存开关；
- Benchmark 是否启用额外的 `timings_per_token` 或 Prompt Progress 参数。

缺失字段标记 unknown；不能用相邻批次数据补齐。

## 5. 验收标准

1. 同一 SHA 下 A～F 微基准可重复运行并保留原始输出。
2. 明确给出单请求与并发路径的 ns/op、B/op、allocs/op；说明测量覆盖与未覆盖的代码边界。
3. 给出事件数/输出长度增长趋势和默认配置的内存上限证据。
4. 真实 NUC 的 TTFT/P50/P95/RSS 结果必须标明 observed；只有存在有效开关对照且条件严格匹配时，才允许给出观测增量结论。
5. 不因测量结果不理想而放宽统计口径；不改变正常推理语义。
6. 若成本处于噪声范围或无法建立可靠因果对照，结论明确为 not_comparable，而不是宣称“零开销”。

## 6. 当前执行顺序

1. PERF-11 Go CI 通过后归档 PERF-11。
2. 补齐 A～F 同版本微基准，复用现有 `performance_benchmark_test.go`，不新增第二套测试框架。
3. 执行固定机器基准并保存原始结果。
4. 根据微基准结果决定是否需要受控端到端 A/B；没有必要时不新增生产开关。
5. 完成真实 NUC 的 observed 分布采样与结论归档。
