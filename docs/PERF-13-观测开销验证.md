# PERF-13 观测开销验证方案

> 状态：进行中（A～F 微基准、事件数阶梯和事件上限回归测试均已通过；思考关闭参数修复已通过 CI，等待 NUC 部署验证后继续 observed 采样）
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
| F | `BenchmarkLatestRequestRuntimeSnapshot` | 测量缓存快照复制与 CPU 状态映射；使用预置缓存，不触发上游请求或 CPU 采样等待 |

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

## 7. 长流阶梯与 Runtime Snapshot 结果（2026-11-23）

### 7.1 执行证据

- Go CI：Run `45136188818`，SHA `ff724b182630857d6190a1dbc05c7e9ec390025d`，6/6 Job success。
- Browser Smoke：Run `45136188864`，同 SHA，success。
- 微基准 Artifact：`perf13-observer-bench`，Artifact ID `14799862757`，1,186 bytes。
- 环境：GitHub Actions Linux amd64，Go 1.27.4，Intel Xeon Platinum 8370C CPU @ 2.80GHz。
- 基准命令：`-benchmem -benchtime=500ms -count=5`；全部基准 PASS，包耗时 34.372 秒。
- 该轮与 6.1 的 AMD EPYC 7763 运行环境不同，结果单独分组，不合并计算或直接比较绝对耗时。

### 7.2 本轮新增基准中位数

| 基准 | ns/op 中位数 | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkRequestTraceRecordStreamChunkWithLiveSession/events_1` | 2,831 | 1,225 | 17 |
| `.../events_100` | 257,965（2,580 ns/chunk） | 85,842 | 1,410 |
| `.../events_2000` | 5,198,764（2,599 ns/chunk） | 1,931,892 | 28,021 |
| `.../events_2100` | 5,598,082（2,666 ns/chunk） | 1,992,757 | 29,421 |
| `BenchmarkLatestRequestRuntimeSnapshot` | 1,143 | 2,272 | 9 |

同一轮既有基准复测：Trace 2,507 ns/op、616 B/op、15 allocs/op；Observer 并发 4,147 ns/op、2,194 B/op、40 allocs/op；DTO 构建 1,084 ns/op、544 B/op、15 allocs/op；JSON 编码 6,185 ns/op、3,842 B/op、7 allocs/op。Consume 下界与前一轮不同 CPU，不能跨机器作差。

### 7.3 事件上限的正确解释

1. 代码中 `requestTrace.appendStreamEvent` 在 `seq >= 2000` 时拒绝新增事件；`live.Session.AppendStreamEvent` 也有 2000 条防线。默认配置关闭 Preview/Raw，另有每请求内容字节预算。
2. 2000 事件基准约 1.93 MB/op；2100 事件约 1.99 MB/op，增加约 60.9 KB/op 和 1,400 次分配，额外 100 个 chunk 仍经过 SSE 解析和请求指标处理。该增量是基准迭代期间的累计分配，不是请求结束后保留的内存。
3. 达到事件上限后继续解析是当前设计行为：Trace 仍需处理后续 chunk 中的 finish/usage/时序等信息，不能直接停止整个解析链路。当前没有证据表明事件列表超过 2000 条，也不能据此认定内存泄漏。
4. 本轮基准显示单次 chunk 解析路径约 2.6～2.7 μs/chunk；它没有测量完整真实模型请求、数据库写入或真实进程 RSS。
5. Runtime Snapshot 读取/映射约 1.14 μs/op、2.2 KB/op；该基准使用预置缓存，不包含后台采样、HTTP 或 CPU 采样等待。

### 7.4 补充回归测试

已在 `internal/server/request_trace_test.go` 增加 `TestRequestTraceCapsStoredStreamEvents`：尝试追加 2,100 条事件，要求 Trace 计数和 Session 实际保留数量均严格为 2,000，并核对序号边界。最新 SHA `8c7d0f210b6f35fb1fc1fdc76747d3d3b7ced5af` 的 Go CI `45137793370` 中，单元/集成、竞态测试和静态检查均通过，该回归测试验收通过。

### 7.5 最新 SHA 微基准复测（2026-11-23）

- Go CI：Run `45137793370`，SHA `8c7d0f210b6f35fb1fc1fdc76747d3d3b7ced5af`，6/6 Job success。
- Artifact：`perf13-observer-bench`，ID `14800626669`，原始文件 `perf13-observer-bench.txt`，已下载并检查，全部 PASS。
- 环境：GitHub Actions Linux amd64，Go 1.27.4，Intel Xeon Platinum 6975P-C；与此前 Xeon 8370C、AMD EPYC 7763 环境均不同，不能直接按绝对值比较。
- 命令：`-benchmem -benchtime=500ms -count=5`，测试包耗时 37.122 秒。

本轮五次采样中位数：

| 基准 | ns/op 中位数 | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkConsumeSSEChunk` | 0.3760 | 0 | 0 |
| `BenchmarkRequestTraceRecordStreamChunk` | 1,636 | 616 | 15 |
| `BenchmarkLiveSessionAppendStreamEvent` | 178.2 | 608 | 2 |
| `BenchmarkInferenceObserverOnChunkParallel` | 2,297 | 2,194 | 40 |
| `BenchmarkBuildRequestAnalysis` | 804.4 | 544 | 15 |
| `BenchmarkEncodeRequestAnalysis` | 4,609 | 3,842 | 7 |
| `.../events_1` | 1,911 | 1,225 | 17 |
| `.../events_100` | 171,771（1,718 ns/chunk） | 85,844 | 1,410 |
| `.../events_2000` | 3,512,709（1,756 ns/chunk） | 1,931,434 | 28,020 |
| `.../events_2100` | 3,674,088（1,750 ns/chunk） | 1,992,213 | 29,420 |
| `BenchmarkLatestRequestRuntimeSnapshot` | 863.5 | 2,272 | 9 |

本轮绝对值比此前 Xeon 8370C 样本低，但硬件不同，不能解释为代码性能改善。每个阶梯的 B/op 是 benchmark 迭代累计分配；不是请求完成后保留内存。Consume 仍只是编译器可优化的空消费下界。

## 8. 真实 NUC observed 采样准备

### 8.1 当前代码可复用的数据入口

1. **Benchmark**：复用管理台现有模型测试/Benchmark 页面和 `/api/benchmark` 创建、`/api/benchmark/{job_id}` 轮询；请求详情使用 `/api/requests/{id}`。不新增压测 API，不要求用户直接调用带密钥的 API。
2. **宿主机资源**：管理员首页 bootstrap 已返回 `host.cpu_usage_percent`、`host.memory`（含 total/used/source）和磁盘信息。CPU 由 `/proc/stat` 计算，是宿主机整体 CPU；内存由 `/proc/meminfo` 的 `MemTotal - MemAvailable` 计算，`source=meminfo`，也是宿主机整体内存。两者都不是 Gateway 容器独占资源或 Gateway 进程 RSS。
3. **llama.cpp 运行状态**：管理台「本地模型」页面的「运行概览」复用 `/api/admin/runtime`，显示模型加载状态和后台 `/metrics` 进程级样本。Metrics 采样器每 5 秒运行一次，独立于请求热路径；请求分析中的 Runtime Snapshot 只附带最近快照，最多 15 秒新鲜度判定，必须结合 `sampled_at` / `age_ms` 解释。
4. **版本身份**：release build 支持注入 Gateway Revision / Modified，但用户现行 ZIP + Compose 构建流程不能假定包含 `.git` 或使用 Git 构建脚本；当前 bootstrap/runtime API 也未暴露可直接核对的线上 revision。若现有镜像标签、digest 或部署记录没有提供身份，就记为 `unknown`。GitHub `dev` SHA 不能代替线上版本，且不要求用户额外手工取 SHA。
5. **RSS 缺口**：当前管理台 API 没有专门的 Gateway 进程 RSS 或 Gateway 容器内存字段。首页 memory 是宿主机整体内存（`/proc/meminfo`），不能替代上述任一指标。后续可查看 1Panel/Docker 是否已有 Gateway 容器内存指标；若能取得，按容器内存 observed 单独记录；没有则标记 `not_collected`。不新增代码采集器，也不把宿主机内存冒充容器内存或进程 RSS。

### 8.2 采样前固定条件

- Gateway 部署身份：优先记录管理台/部署记录中已有的镜像标签或 digest；当前管理 API 未暴露可核对的 Gateway Revision。用户现行 ZIP + `docker compose up -d --build` 流程不能假定 ZIP 内含 `.git`；`scripts/build-gateway-image.sh` 也要求 Git 工作区，不能假定实际部署使用了该脚本。因此线上 revision 无证据时标记 `unknown`，不要求用户手工获取 SHA，也不阻塞 observed 采样。不得把 GitHub `dev` 最新 SHA 当成线上版本。
- NUC 硬件：i5-1340P、32 GB RAM；CPU-only 或实际 backend 必须以运行时证据为准。
- llama.cpp 镜像 digest、版本、启动参数（尤其线程、ctx、batch/ubatch、metrics），Gateway 活动 Provider 与模型 ID。
- 本轮实际活动模型为 Qwen3.5-4B-IQ4_NL；模型文件身份/量化 SHA 未取得，记为 unknown，不阻塞 observed，但不补猜。
- Gateway 配置：请求超时、warm start、日志 payload/stream raw/preview 设置；敏感密钥和完整环境变量不采集、不上传。
- Benchmark 条件：固定 prompt 数据集版本、temperature/top_p/seed、max_tokens、并发 1；记录实际输入/输出 Token、TTFT、总耗时、原生 Prompt/Decode timings 和缓存分类。
- 先预热模型，再串行执行；采样窗口内不发其他推理请求。先完成少量 smoke 请求确认指标完整，再按固定样本重复采集，不盲目并发压测。

### 8.3 本轮允许输出的结论

本轮只报告实际运行环境下的 observed：TTFT、总延迟、输入/输出 Token、Prompt/Decode timings、缓存状态、宿主机 CPU/内存和 llama.cpp Metrics；如有独立 1Panel/Docker 指标，再单列 Gateway 容器内存。由于当前没有关闭请求观测的生产对照开关，且不应为本任务关闭审计/请求关联，不能从两批不同时间的请求推导观测开销因果增量。没有 Gateway 进程 RSS 数据时，明确记为 unavailable/not_collected。

### 9.1 思考关闭修复后的 NUC Smoke（2026-11-23）

- Request ID：`req_1795426066335852781_bf7024a377`；Gateway / upstream HTTP 200，状态 success；模型 `Qwen3.5-4B-IQ4_NL`，流式请求。
- 上游请求体已同时包含 `reasoning_effort: "none"` 和 `chat_template_kwargs.enable_thinking: false`，不再出现顶层 `enable_thinking: false`。
- 上游 SSE 实际返回均为 `delta.content`，未观察到 `reasoning_content`；说明本次「关闭思考」请求行为符合预期，修复已通过真实 NUC 请求验证。
- 总耗时 969 ms；TTFT / 首条正文 263 ms；正文生成阶段 706 ms；输入 13 tokens、输出 10 tokens；llama.cpp Prompt 256.489 ms（6 prompt tokens，23.39 tok/s），Decode 706.193 ms（10 tokens，12.74 tok/s）。
- Runtime Snapshot：`complete`，活动模型正确，采样年龄 970 ms；宿主机 CPU 1.25%。本次输入/输出均较短，只作为部署 Smoke，不作为性能分布结论。
- 本次采样的 `cache_n=7`，存在缓存命中；不能将它当作冷缓存样本，也不能与无缓存样本混合计算同一组性能分布。
- 结论：思考关闭参数修复通过代码 CI 和真实 NUC 请求验证。下一步固定模型、参数、提示词与输出目标，分开记录缓存命中/未命中样本，完成串行重复采样；不重跑 PERF-12。

## 10. Benchmark 条件完整度规则修正（2026-11-23）

### 10.1 问题与结论

NUC 串行采样的 10 条任务均为 completed，但条件状态为 partial，缺失项只有：

- model_sha256_unknown
- gateway_revision_unknown
- gateway_build_cleanliness_unknown

此前实现将“性能比较条件完整度”和“实验制品追溯完整度”混为一谈，导致全部样本被排除。该判定过严，需要区分运行条件与追溯信息。

### 10.2 新规则

以下信息缺失只降低复现追溯等级，不单独阻断同条件性能比较：

- 模型文件 SHA-256：只要 Backend 实际模型 ID 可确认，允许使用该 ID 作为本轮运行身份；不能据此声称模型文件字节级一致。
- Gateway Revision：缺失时不阻断，但不能声称已确认线上代码版本。
- Gateway 构建洁净状态未知：缺失时不阻断；若明确检测到构建包含未提交修改，仍作为比较阻断项。

以下运行条件仍是比较门槛：Backend、实际模型 ID、Prompt SHA-256、Backend 版本、测试前模型加载状态、CPU 型号、内存快照。任一关键项缺失，仍标记 partial 并阻止自动合并。

比较仍要求任务 completed、场景相同、条件指纹非空且一致。比较接口区分 strict 与 limited：存在上述追溯缺口时标记 limited，不再错误展示为严格可比。

### 10.3 模型标识语义

- Gateway 的 models.model_key 是数据库唯一业务键（全局 UNIQUE），用于定位 Gateway 管理的模型记录。
- llama.cpp 请求实际使用的 model_id 是 Backend 注册模型标识；它不是 Gateway model_key，也不是文件 SHA-256。
- 条件指纹以 Backend、实际 model_id 和推理配置为运行身份依据；model_key 作为 Gateway 别名保留在快照中，但不单独决定模型内容身份。
- 当 Benchmark 通过 active model / model_id 路径运行且没有关联到 Gateway Model 记录时，model_key 为空不代表 llama.cpp 模型未知；只要实际 model_id 有值，仍可按有限追溯条件比较。

### 10.4 历史记录兼容

历史任务不做数据迁移。比较读取 ConfigJSON 时，若旧记录的 condition_status=partial 且 condition_missing 仅包含上述三种追溯缺口，则按可比较运行条件重新分类；其他 partial 记录继续阻断。历史原始 condition_status 与缺失字段仍保留在 ConfigJSON 中。

### 10.5 当前 10 次采样的处理

本批 10 次采样保留原始记录，不重新执行。用户提供的第一条任务已确认：
- Backend llamacpp、实际 model_id Qwen3.5-4B-IQ4_NL；
- 测试前模型状态 loaded；
- Backend 版本、CPU、内存等条件未列入 condition_missing；
- 缺失项仅为三种追溯信息。

因此该样本属于有限追溯、可按相同指纹与其余样本分组的 observed 数据；仍不能称为模型文件 SHA 和线上 Gateway 版本均已确认的严格可复现基线。

## 9. 下一步执行顺序

1. 打开 NUC 当前管理台，确认当前活动 Backend、模型标识和模型已加载状态；部署 revision 无现成证据时记为 `unknown`，不额外取 SHA、不阻塞采样。
2. 在「本地模型 → 运行概览」查看 llama.cpp Server Metrics，展开动态指标，记录 `sampled_at`、采样状态、进程实例标识和指标值；采样器约每 5 秒刷新一次，读取时必须记录样本时间，不能把页面打开时间当作指标采样时间。
3. 在首页记录宿主机 CPU 与宿主机内存 used/total/source。当前实现的 CPU 来源为 `/proc/stat`，内存来源为 `/proc/meminfo`（`source=meminfo`）；两者均为宿主机口径。Gateway 容器内存和进程 RSS 需从 1Panel/Docker 独立指标取得；拿不到就分别记为 `not_collected`。
4. 已完成思考关闭修复部署验收：Request ID `req_1795426066335852781_bf7024a377` 的上游 body 包含 `reasoning_effort=none` 和 `chat_template_kwargs.enable_thinking=false`，SSE 只有正文 content，无 reasoning_content。该 Smoke 已记录在 9.1。不要重跑 PERF-12 四组固定套件，也不要覆盖其历史报告。
5. Smoke 数据完整后，在同一模型、参数、输入长度、输出上限和并发条件下串行采集重复样本；每次记录请求详情和采样前后 Runtime。测试窗口内不发送其它推理请求。若 UI 没有批量重复入口，不新增脚本或 API，先以现有普通 Benchmark 单次任务入口逐次执行。
6. 根据 observed 证据报告 TTFT/总耗时分布、Prompt/Decode timings、Metrics 窗口、宿主机 CPU/内存；若 1Panel/Docker 提供独立指标，再单独报告 Gateway 容器内存。没有观测关闭对照时，不声称观测增量；无法建立严格匹配对照时标记 `not_comparable`。


## 10. 自动串行采样工具

- 普通性能测试入口保留单次执行，并增加可填写重复次数（1～20，默认 10）的串行采样入口。
- 批量执行复用现有 Benchmark 创建与轮询；每次独立创建 Job，等待上一 Job 终态后才启动下一次。若任务详情无法确认终态，则停止批次，避免请求重叠。
- 每次记录 Job ID、状态、条件状态、TTFT、总耗时、Token、Prompt/Decode timings、吞吐和缓存分类；不复制或持久化 Prompt、回答、思考正文。
- 报告在浏览器本地保存轻量指标，可复制 Markdown；每个 Job 的完整分析仍由现有 Benchmark 历史详情提供。
- 指标汇总仅纳入状态为 `completed`、`condition_status=complete` 且非空 `condition_sha256` 完全一致的样本；条件不完整、指纹缺失或不同，不做合并统计。检测到模型或条件变化时停止后续采样，并禁止整批合并统计。缓存状态分组统计，不混合冷缓存与命中缓存样本。
- 停止操作阻止后续采样，并在有活动 Job 时取消该 Job；已完成 Job 与已记录结果保留。
- 工具代码与自动化测试通过后，仍需部署到 NUC 并执行实际采样；不得把 UI 工具完成误报为 PERF-13 真实环境验收完成。


## 11. 自动串行采样工具 CI 验收结果

- 最终代码提交：`66bb2877d964b3f32f85b76276af63b2ca92196f`（分支 `dev`）。
- Go CI：Run `45155196642`，六个 Job 全部成功；覆盖前端构建/单元测试、Go 单元集成与竞态测试、PERF-13 微基准、Gateway 故障注入、并发审计负载、Docker 镜像与漏洞扫描。
- Web UI Browser Smoke：Run `45155196735`，成功；报告 215/215 项通过，新增重复采样入口、次数边界、报告展示/复制和不持久化提示词检查全部通过。
- 这两项是 CI 环境的功能与浏览器验收，不代表 NUC 上已部署，也不代表真实推理采样已完成。NUC 部署与真实采样仍是 PERF-13 下一步。
