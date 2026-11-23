# PERF-13 观测开销验证方案

> 状态：已完成（A～F 微基准、长流事件阶梯、事件上限回归、两批 NUC 10 次串行 observed 采样、条件可比性修正、部署后有限可比验收、Go CI 与 Browser Smoke 均已完成）
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

### 10.6 当前状态与下一步处理

截至 2026-11-23，PERF-13 已完成代码侧和 CI 侧工作：

- 条件可比性修正已提交；旧历史记录在读取时按新规则动态重分类，不执行数据库迁移，也不改写原始 ConfigJSON。
- 本轮代码/工作流提交：`4318d2d3d57271ce047efa58abe3f61bf6b160c1`（分支 `dev`）。
- Go CI：Run `45165100163`，6/6 Job success。
- Browser Smoke：Run `45165100091`，success；报告 215/215 项通过，包含 PERF-12 Suite 状态机、桌面与 390px 窄屏页面、登录态、密钥隔离、模型测试入口、重复采样和历史详情交互。截图与报告 Artifact：`web-ui-browser-smoke`，ID `14813593064`。
- Browser Smoke 安装步骤已拆分为 Playwright 包安装和 Chromium/系统依赖安装，增加浏览器缓存及独立超时；本轮安装和浏览器验收均成功。

部署后验收已通过。用户在部署修正版本后重新执行一批 10 次串行采样，10/10 服务端完成、10/10 同条件可比、条件指纹分组 1、可比等级为 limited；每条任务的 `condition_status=complete`。这确认最新部署的条件分类和串行汇总链路正常工作。

此前旧批次的动态重分类逻辑仍由代码兼容路径处理：仅当历史 `condition_status=partial` 且缺失项完全属于三项追溯信息时，读取时动态归类；原始 ConfigJSON 不迁移、不改写。最新部署后的新批次报告本身不能替代对旧记录的单独读取，但旧记录兼容规则已由代码与 CI 验收覆盖，不再要求用户为此重复采样或额外执行人工检查。

本任务没有关闭观测链路的 A/B 对照，因此最终结论仍不得声称已测得观测链路对 TTFT/总延迟的因果增量；本任务以完成现有可取得证据的测量、记录限制并确认部署后的有限可比链路为完成标准。

## 11. 自动串行采样工具

- 普通性能测试入口保留单次执行，并增加可填写重复次数（1～20，默认 10）的串行采样入口。
- 批量执行复用现有 Benchmark 创建与轮询；每次独立创建 Job，等待上一 Job 终态后才启动下一次。若任务详情无法确认终态，则停止批次，避免请求重叠。
- 每次记录 Job ID、状态、条件状态、TTFT、总耗时、Token、Prompt/Decode timings、吞吐和缓存分类；不复制或持久化 Prompt、回答、思考正文。
- 报告在浏览器本地保存轻量指标，可复制 Markdown；每个 Job 的完整分析仍由现有 Benchmark 历史详情提供。
- 指标汇总仅纳入状态为 `completed`、`condition_status=complete` 且非空 `condition_sha256` 完全一致的样本；条件不完整、指纹缺失或不同，不做合并统计。检测到模型或条件变化时停止后续采样，并禁止整批合并统计。缓存状态分组统计，不混合冷缓存与命中缓存样本。
- 停止操作阻止后续采样，并在有活动 Job 时取消该 Job；已完成 Job 与已记录结果保留。
- 工具代码与自动化测试通过后，用户已在 NUC 执行 10 次真实串行采样；结果与统计见第 13 节。不得因旧版本地批次报告的汇总缺失而要求用户重复采样。


## 12. 自动串行采样工具 CI 验收结果

- 最终代码提交：`66bb2877d964b3f32f85b76276af63b2ca92196f`（分支 `dev`）。
- Go CI：Run `45155196642`，六个 Job 全部成功；覆盖前端构建/单元测试、Go 单元集成与竞态测试、PERF-13 微基准、Gateway 故障注入、并发审计负载、Docker 镜像与漏洞扫描。
- Web UI Browser Smoke：Run `45155196735`，成功；报告 215/215 项通过，新增重复采样入口、次数边界、报告展示/复制和不持久化提示词检查全部通过。
- 这两项是采样工具交付前的 CI 功能与浏览器验收记录；后续 NUC 部署和 10 次真实推理采样已完成，见第 13 节。


## 13. NUC 10 次串行采样与 PERF-13 收尾记录（2026-11-23）

### 12.1 采样结果

用户已在 NUC 完成 10 次串行 Benchmark。10 条任务均 completed、HTTP 200，无错误；输入/输出 Token 均为 13/10，缓存状态均为“部分命中”。旧版汇总规则因将追溯缺口视为比较阻断项，曾显示同条件可比样本为 0；这不代表 10 条请求失败，也不代表运行条件发生变化。

按 10 条原始采样值复算，统计如下：

| 指标 | 最小值 | 中位数 | 最大值 |
|---|---:|---:|---:|
| 总耗时（ms） | 826 | 857 | 874 |
| TTFT（ms） | 139 | 156.5 | 173 |
| Prompt（ms） | 133.69 | 150.235 | 167.00 |
| Decode（ms） | 673.23 | 698.09 | 730.12 |
| 输出速度（tok/s） | 12.33 | 12.895 | 13.37 |

本批所有样本均为部分缓存命中，不是冷缓存基线，也不是完整缓存命中基线。它们可以按相同运行条件指纹进行 limited observed 分析，但模型文件 SHA-256、线上 Gateway Revision 和构建洁净状态未知，不能称为字节级严格可复现基线。

### 12.2 结果解释边界

1. 总耗时中位数约 857 ms，TTFT 中位数 156.5 ms；Decode 中位数约 698.09 ms，是本批请求耗时的主要阶段。
2. 以上数据描述的是当前 NUC 部署和部分缓存条件下的观测分布，不是关闭观测链路前后的 A/B 对照。
3. 微基准覆盖 SSE 解析、事件追加、Observer、RequestAnalysis DTO/JSON 编码和 Runtime Snapshot 读取；它们不能简单相加，也不能直接换算成真实模型请求的 TTFT 或总耗时增量。
4. 当前没有可靠证据证明请求观测链路是 NUC 推理性能瓶颈；不应仅凭 Decode 耗时占比或 JSON 编码微基准启动优化。
5. Gateway 进程 RSS / 容器独占内存没有可靠采集证据，继续标记 unavailable/not_collected；不得用宿主机内存替代。

### 13.3 第一批采样结论

- **真实 NUC observed：** 第一批 10 次串行采样均已完成，旧版汇总器因条件分类规则过严而显示 0 个可比样本；原始任务数据仍然有效。
- **条件比较规则：** 已修复并通过 Go CI 与 Browser Smoke；三项追溯缺口只降低复现等级，不单独阻断比较。
- **不作出的结论：** 第一批数据不能证明“观测零开销”，也不能量化观测链路对 TTFT/总延迟的因果增量。

### 13.4 部署修正版本后的第二批 10 次串行采样

用户在部署修正版本后重新执行 10 次串行 Benchmark，报告结果如下：

- 报告状态：completed；计划次数 10，实际尝试 10，服务端完成 10。
- 同条件可比样本：10；条件指纹分组：1；可比等级：有限可比（追溯信息不完整）。
- 10 条任务的 `condition_status` 均为 `complete`，无错误；输入/输出 Token 均为 13/10。
- 缓存状态均为部分命中。
- 采样时间：`2026-11-23T11:36:23.530Z` 至 `2026-11-23T11:36:45.399Z`。

| 指标 | 有效样本数 | 最小值 | 中位数 | 最大值 |
|---|---:|---:|---:|---:|
| 总耗时（ms） | 10 | 822.00 | 864.50 | 917.00 |
| TTFT（ms） | 10 | 139.00 | 160.00 | 168.00 |
| 输出吞吐（tok/s） | 10 | 11.91 | 12.79 | 13.23 |
| Prompt 吞吐（tok/s） | 10 | 24.82 | 25.96 | 29.69 |
| Decode 吞吐（tok/s） | 10 | 11.91 | 12.78 | 13.23 |

逐条任务：

| 次数 | Job ID | 总耗时 ms | TTFT ms | Prompt ms | Decode ms | 输出 tok/s |
|---:|---|---:|---:|---:|---:|---:|
| 1 | `req_1795433783727324290_26fac6323d` | 894 | 163 | 156.53 | 730.56 | 12.32 |
| 2 | `req_1795433785872388231_4017068483` | 917 | 161 | 154.86 | 755.86 | 11.91 |
| 3 | `req_1795433788038409473_45f9890233` | 913 | 163 | 156.94 | 749.47 | 12.01 |
| 4 | `req_1795433791006637889_9745609445` | 847 | 159 | 153.37 | 687.30 | 13.09 |
| 5 | `req_1795433792812128603_2391f3e661` | 889 | 168 | 161.15 | 720.24 | 12.50 |
| 6 | `req_1795433794783920075_34397b4847` | 882 | 161 | 155.21 | 720.56 | 12.49 |
| 7 | `req_1795433797297249905_697a1d09e0` | 826 | 146 | 141.20 | 680.29 | 13.23 |
| 8 | `req_1795433799386444758_65b955d01a` | 833 | 144 | 137.22 | 688.62 | 13.07 |
| 9 | `req_1795433801195479892_2da0ec90c2` | 828 | 146 | 140.74 | 681.13 | 13.21 |
| 10 | `req_1795433803220149783_08dca67198` | 822 | 139 | 134.74 | 682.67 | 13.18 |

本批结果证明部署修正版本后的新任务可正确分类并进入有限可比汇总。有限可比表示三项追溯信息仍不完整，不是严格可复现基线。所有样本均为部分缓存命中，不能作为冷缓存或完整缓存基线。

第二批与第一批是不同时间窗口的独立采样批次；现有报告没有提供跨批次相同的 `condition_sha256` 证据，因此只分别归档，不将两批样本合并，也不据此推断性能发生变化。

### 13.5 PERF-13 最终结论

- **微基准与长流边界：** 已完成；现有数据未证明观测链路存在需要立即优化的明显瓶颈。
- **真实 NUC observed：** 两批各 10 次串行采样均已归档；第二批 10/10 同条件可比，全部为部分缓存命中。
- **条件比较规则：** 已修复；第二批真实部署报告确认 `complete` 条件状态、单一指纹分组和 limited 可比等级符合预期。
- **部署验收：** 已通过；无需继续重复采样。
- **不作出的结论：** 当前不能声称“观测零开销”，也不能声称已量化观测链路对 TTFT/总延迟的因果增量。Gateway 进程 RSS / 容器独占内存仍没有可靠采集证据，维持 unavailable/not_collected。

