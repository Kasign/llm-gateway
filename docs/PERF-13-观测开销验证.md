# PERF-13 观测开销验证方案

> 状态：进行中（A～E 微基准首轮已完成；待评估长流/内存阶梯与真实 NUC observed 采样）
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
| A | `BenchmarkConsumeSSEChunk`：仅消费 chunk，不解析/记录 | 测量基准开销 |
| B | `BenchmarkRequestTraceRecordStreamChunk`：requestTrace.recordStreamChunk | 测量 SSE 解析与 Trace 事件构造增量 |
| C | `BenchmarkLiveSessionAppendStreamEvent`：Live Session 追加事件 | 测量单条事件追加、锁和分配成本 |
| D | `BenchmarkInferenceObserverOnChunkParallel`：inferenceObserver.onChunk，事件预算已耗尽 | 测量长流稳态并发路径 |
| E | `BenchmarkBuildRequestAnalysis` + `BenchmarkEncodeRequestAnalysis` | 分别测量 DTO 构建和 JSON 编码 |
| F | Runtime Snapshot 读取/映射 | 测量请求收尾快照开销；目前尚无独立基准，需先确认采样器 nil/不可用分支和 host CPU 采样依赖 |

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

## 6. 首轮微基准结果（2026-11-23）

### 6.1 执行证据

- GitHub Actions Go CI：Run `45133513574`，提交 `b79f64967ef4bc8a843af9c0f40b6e0fd5b75deb`，6/6 Job success。
- Go 测试 Job 中 PERF-13 微基准步骤 success，命令使用 `-benchmem -benchtime=500ms -count=5`。
- 原始结果 Artifact：`perf13-observer-bench`，Artifact ID `14799156039`，大小 785 bytes，保留期 30 天。
- 环境：GitHub Actions Linux amd64，Go 1.27.4，AMD EPYC 7763 64-Core Processor。
- 测试包：`internal/server`；全部基准 PASS，测试进程总耗时约 20.346 秒。

### 6.2 结果摘要

以下为每项 5 次采样的中位数；区间为五次原始样本的最小值至最大值。

| 基准 | ns/op 中位数 | ns/op 范围 | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `BenchmarkConsumeSSEChunk` | 0.6349 | 0.6289–0.6399 | 0 | 0 |
| `BenchmarkRequestTraceRecordStreamChunk` | 2,560 | 2,549–2,619 | 616 | 15 |
| `BenchmarkLiveSessionAppendStreamEvent` | 204.9 | 201.9–249.6 | 608 | 2 |
| `BenchmarkInferenceObserverOnChunkParallel` | 3,740 | 3,670–3,867 | 2,194（个别样本 2,193） | 40 |
| `BenchmarkBuildRequestAnalysis` | 1,145 | 1,130–1,179 | 544 | 15 |
| `BenchmarkEncodeRequestAnalysis` | 6,190 | 6,146–6,386 | 3,842 | 7 |

原始逐次结果以 GitHub Actions Artifact 为准；本表不替代原始输出。

### 6.3 初步解释与限制

1. 在该 CI CPU 上，Trace 记录路径约 2.56 μs/op，Live Session 单事件追加约 205 ns/op，并发 Observer 稳态路径约 3.74 μs/op。
2. RequestAnalysis DTO 构建约 1.15 μs/op；JSON 编码约 6.19 μs/op、3.8 KB 分配，是本组独立微基准中单次耗时和分配量较高的步骤。
3. `BenchmarkConsumeSSEChunk` 仅作为循环与全局 sink 写入的下界，不是与 Trace 完全等价的真实请求基线；不能将各基准简单相减后宣称为观测增量。
4. 并发 Observer 基准测的是事件预算已耗尽后的稳态路径，不覆盖完整请求生命周期、数据库审计写入、模型网络等待或完整流式响应。
5. 以上数据来自共享 CI 虚拟机，不代表用户 NUC 的绝对性能；微基准也不能直接推导 TTFT、P50/P95 或 RSS 的因果增量。
6. 当前结果没有证明观测链路构成实际瓶颈，不能仅凭 JSON 编码相对较高就启动优化。

## 7. 下一步执行顺序

1. 审查并补齐 A～E 的测试覆盖边界；重点核对默认配置、事件预算上限前后、长流增长与分配情况，复用现有测试文件，不新建基准框架。
2. 评估 F Runtime Snapshot 是否能在无副作用条件下隔离；不能安全隔离就记录 not_comparable，不强行造基准。
3. 若代码路径测试未显示明显高成本，再在真实 NUC 上采集正常部署的 TTFT/P50/P95/RSS observed 分布；固定模型、参数、缓存分类、并发和版本条件。
4. 当前没有关闭核心请求观测的生产 A/B 开关。真实 NUC 分布只能描述运行状态，不能声称因果观测增量；未经独立设计评审，不新增生产总开关，也不关闭鉴权、错误审计或请求关联能力。
5. 根据长流/内存阶梯与 NUC observed 结果决定是否存在值得优化的具体瓶颈；若无法形成有效因果对照，结论明确为 not_comparable。
