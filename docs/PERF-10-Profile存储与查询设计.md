# PERF-10 Profile 存储与查询设计

> 状态：设计审计完成，实施待开始
> 日期：2026-11-23
> 分支：dev
> 状态权威来源：`docs/整体任务规划.md`

## 1. 目标与非目标

PERF-10 的目标是让现有请求分析数据具备清晰的数据档位、可查询性和生命周期治理，为 PERF-11 的 Benchmark ↔ Profile 关联提供稳定基础。

本任务不新增第二套请求分析采集链路，不复制 RequestAnalysis JSON，不建设独立 Profile 数据库，也不提前实现 PERF-08 尚未具备的 GGML Deep Profile 数据。

## 2. 当前实现事实

审计 `dev` 当前代码确认：

1. `request_logs.analysis_json` 已保存请求级 `RequestAnalysis`，由 `persistRequestAnalysis` 在请求结束时写入；没有单独 Profile 表。
2. `request_logs` 是请求分析的现有权威记录。Store 已有 `SaveRequestAnalysis` / `GetRequestAnalysis`，Benchmark 恢复路径通过 Run.RequestID 读取同一份分析。
3. `benchmark_runs.request_id` 保存请求关联，但数据库没有到 `request_logs` 的外键。
4. 请求保留任务按 `started_at` 删除终态 `request_logs`，并显式删除请求事件和 Payload；它不检查 `benchmark_runs` 是否仍引用该 RequestID。
5. Benchmark Task/Run 有独立的 `DeleteFinishedBefore` 存储方法，但当前审计未发现与请求保留任务形成统一生命周期策略的证据。
6. 当前没有独立 Profile API 或 Profile 页面；请求详情和 Benchmark 恢复接口已能读取部分既有 RequestAnalysis。

## 3. 设计决策

### 3.1 Profile 是分析视图，不是重复实体

PERF-10 不新增 Profile 主表。Profile 的稳定身份沿用 `request_id`，数据继续由 `request_logs.analysis_json` 承载；查询层以现有 RequestLog 为入口，按需读取 Analysis JSON。

原因：
- 一次请求只有一份请求级分析；复制进新表会引入双写、事务一致性和迁移复杂度。
- BenchmarkRun 已经持有 request_id，沿用它即可关联 Profile。
- 当前 RequestAnalysis 是 JSON 快照，PERF-08 Deep Profile 尚未形成可采集的结构化数据源，不能先建立空泛的通用 Profile schema。

### 3.2 三档生命周期语义

| 档位 | 数据范围 | 默认行为 | 生命周期 |
|---|---|---|---|
| default | 请求摘要、核心 Token/Latency、必要的轻量分析字段 | 普通请求默认档 | 跟随现有请求日志保留配置 |
| diagnostic | Request Timeline、Timing、Runtime Snapshot、必要的 Metrics 证据 | 只在已有诊断/Benchmark 采集条件下保存；不得为普通请求增加同步上游探测 | 由诊断保留期限控制；第一阶段可复用 RequestAnalysis 同记录，不单独复制 |
| deep | PERF-08 可安全采集的受控样本深度数据 | 默认关闭；当前无已验收的可用数据源 | 独立短 TTL、采样比例和大小上限；PERF-08 未恢复前不落地实现 |

档位描述的是数据范围和保留策略，不意味着必须建立三张表或三个独立 API。当前代码尚未提供可靠的 per-request tier 字段，因此不能仅凭请求类型推断 diagnostic/deep。实施时只有确实需要用户可配置的档位控制时才增加最小字段。

### 3.3 保留一致性

必须解决 Benchmark 历史与请求分析的生命周期冲突：

- Benchmark Task/Run 仍保留时，其 RequestID 对应的核心 RequestLog/RequestAnalysis 不应被普通请求清理策略提前删除。
- Payload、原始 SSE、预览、工具事件属于更高敏感度/更大体积的数据，不应因为 Benchmark 保留而无限期保留。
- 当 Benchmark Task 到期删除时，若 RequestLog 已超过请求保留期，再由清理任务回收不再被其他 Benchmark Run 引用的 RequestLog 及子记录。
- 清理必须批次化、事务化，活动请求不删除；共享 RequestID（如有）必须先检查引用数。
- 不得把“保留 Benchmark 摘要”误认为“必须永久保留完整请求正文”。

### 3.4 查询边界

第一阶段复用现有请求详情读取能力，不增加与现有接口重复的 Profile 查询 API。若现有详情接口无法在请求记录保留后返回 Benchmark 所需分析，再补充只读查询能力，返回：
- request_id
- 分析档位/可用性状态
- RequestAnalysis（缺失时明确 not_available / expired）
- 数据时间与采集来源

不返回 Prompt/Response/Reasoning 正文，不在查询时重算历史指标，不用相邻阶段耗时补齐缺失值。

## 4. 实施顺序

1. 先修复请求与 Benchmark 的保留一致性：请求清理跳过仍被保留 BenchmarkRun 引用的 RequestID；Benchmark 过期清理后允许回收已超期且不再被引用的请求记录。
2. 核实并复用现有请求详情 API 对 RequestAnalysis 的查询能力；缺口才增加查询字段或 endpoint。
3. 增加 Store 层针对引用保护与清理顺序的测试，覆盖并发/批次边界、Benchmark 到期、请求已超期、活动请求保护。
4. 更新 OpenAPI 与管理台仅在接口契约确有变化时同步，不为“Profile”概念另建平行页面。
5. PERF-10 完成后，PERF-11 只需使用 BenchmarkRun.RequestID 关联该请求分析，不再增加重复关联表。

## 5. 当前阻塞与风险

- Benchmark 清理调度是否已接入 Server 生命周期仍需从启动调用链进一步核实；仅有 Store 删除方法不能视为自动清理已运行。
- 请求详情 API 的分析字段和鉴权范围需要进一步核实，避免误判“没有 Profile API”等同于“没有分析查询能力”。
- 若请求日志清理任务和 Benchmark 清理任务并发执行，必须以数据库事务/锁定和引用检查确保不会先删掉仍被引用的 RequestLog。
- 不引入新的配置项，直到默认请求、诊断请求和深度请求的真实采集差异被代码证据证明需要独立配置。

## 6. 验收标准

- 不重复保存 RequestAnalysis。
- BenchmarkRun 在其保留期内可通过 RequestID 读取对应分析；分析不可用时返回明确状态。
- 请求清理不会破坏仍在保留期的 Benchmark 分析关联。
- Benchmark 到期后，过期请求分析可以被回收；不残留孤儿事件/Payload。
- 清理仍保持批次、事务和活动请求保护。
- 单元测试、MariaDB 集成测试、Go CI 和 Browser Smoke 通过。
- 不增加普通推理请求的上游探测或明显热路径开销。
