# PERF-10 Profile 存储与查询设计

> 状态：已完成（生命周期、分析可用状态、OpenAPI 契约均已通过验收）
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
5. Benchmark Task/Run 有独立的 `DeleteFinishedBefore` 存储方法；Gateway 启动链路此前只启动请求清理，没有调度 Benchmark 到期清理。PERF-10 已将 Benchmark 清理接入同一请求保留周期，并先删到期 Task/Run，再清理请求记录。
6. 当前没有独立 Profile API 或 Profile 页面；现有 `/api/requests/{request_id}` 详情接口已经能返回 RequestAnalysis，Benchmark 恢复接口也能按 Run.RequestID 读取分析。因此不新增 Profile 查询 API。

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
- 当前实施顺序为：同一轮清理先按统一 cutoff 删除到期 Benchmark Task（Run 由外键级联删除），再清理过期 RequestLog。Store 层额外使用 `NOT EXISTS` 检查，任何仍被 BenchmarkRun 引用的请求都不会被请求清理删除。该修复已通过 MariaDB 集成验收、竞态测试与静态检查。
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
2. 已确认并复用现有 `/api/requests/{request_id}` 请求详情 API；响应增加 `analysis_status`，区分 `persisted`、`reconstructed`、`unavailable`，不新增 endpoint。Benchmark 持久化恢复响应增加 `analysis_status`，区分 `persisted`、`not_recorded`、`request_log_missing`、`read_failed`、`invalid`、`store_unavailable`。
3. 已增加 MariaDB 集成测试覆盖 Benchmark 引用期间保护请求记录、Benchmark 到期后再清理请求；新增 Benchmark 分析状态分类单元测试。最新 Go CI `45130146800`（SHA `e9e96ead`）6/6 Job 成功，覆盖 MariaDB 生命周期、竞态、静态检查、OpenAPI 契约、前端构建和 Docker 镜像。
4. 更新 OpenAPI 与管理台仅在接口契约确有变化时同步，不为“Profile”概念另建平行页面。
5. PERF-10 完成后，PERF-11 只需使用 BenchmarkRun.RequestID 关联该请求分析，不再增加重复关联表。

## 5. 剩余边界（不阻塞 PERF-10）

- Benchmark 清理已接入现有请求保留后台任务，与请求日志共用 `logging.retention_days`，不新增调度器或独立期限配置。
- 当前数据库未建立 `benchmark_runs.request_id` 到 `request_logs.request_id` 的外键；通过请求清理事务内的引用检查保护关联。PERF-10 不为此引入额外迁移，后续如需强化数据库级约束，应单独评估历史孤儿数据与迁移风险。
- Deep Profile 数据源仍属于 PERF-08 范围；当前无已验收的 operation/backend 级 per-request 数据，因此本任务不建立空泛字段或采集链路。
- default/diagnostic/deep 目前是数据范围与保留语义，不强行增加未被现有业务证明需要的配置字段。

## 6. 验收结果

- 生命周期：MariaDB 集成测试验证 Benchmark 引用期间请求记录受保护；Benchmark 到期后允许请求记录按原 TTL 回收。
- 并发与代码质量：最新 Go CI `45130146800` 对应 SHA `e9e96eadff672717344966253fb239ca444221bb`，6/6 Job 成功；包括竞态测试与静态检查。
- API 契约：RequestDetail 与 BenchmarkStatus 的分析状态字段已同步 OpenAPI，契约检查通过。
- 浏览器回归：Browser Smoke `45129981162`（SHA `0e87a421`）成功；其后至 `e9e96ead` 的变更为后端状态分类测试、OpenAPI 和文档，不包含前端功能代码变更。
- PERF-10 不新增 Profile 表、独立 Profile API 或重复 RequestAnalysis 存储；验收完成。

## 7. 验收标准

- 不重复保存 RequestAnalysis。
- BenchmarkRun 在其保留期内可通过 RequestID 读取对应分析；分析不可用时返回明确状态。
- 请求清理不会破坏仍在保留期的 Benchmark 分析关联。
- Benchmark 到期后，过期请求分析可以被回收；不残留孤儿事件/Payload。
- 清理仍保持批次、事务和活动请求保护。
- 单元测试、MariaDB 集成测试、Go CI 和 Browser Smoke 通过。
- 不增加普通推理请求的上游探测或明显热路径开销。
