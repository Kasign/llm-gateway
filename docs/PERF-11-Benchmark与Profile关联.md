# PERF-11 Benchmark ↔ Profile 关联

> 状态：已完成
> 日期：2026-11-23
> 分支：dev
> 状态权威来源：`docs/整体任务规划.md`

## 1. 目标

让 Benchmark 的实时结果、历史详情和完整请求分析形成可追溯闭环：从 Benchmark Run 可以查看同一请求的 RequestAnalysis，也可以进入完整请求详情；反向通过 RequestID 能定位到所属 Benchmark Run（仅在已有查询路径支持时复用，不为对称性强行增加接口）。

## 2. 已有实现审计

- `benchmark_runs.request_id` 已保存请求关联，Run.RequestID 是唯一关联键。
- `GET /api/benchmark/{job_id}` 从持久化 Run 读取 RequestID，并按该 ID 读取 RequestAnalysis；响应包含 `request_id`、`request_log`、`analysis_status` 和可用时的 `analysis`。
- Benchmark 历史详情 `renderBenchmarkHistoryDetail` 已展示阶段耗时、吞吐、原生 llama.cpp timings、测试条件与请求元数据；但此前没有从历史详情跳转到完整请求详情的操作。
- Benchmark 当前执行结果页已有“完整调用记录”入口，可调用 `showDetail(request_id)`。
- `GET /api/requests/{request_id}` 返回同一份 RequestAnalysis，并标记 `persisted`、`reconstructed` 或 `unavailable`；请求详情 UI 已展示分析内容和状态说明。
- 不新增 Profile 表、关联表、独立 Profile API，不复制 RequestAnalysis JSON，不改变 RequestID 生成规则。

## 3. 本轮实施

1. 在 Benchmark 历史详情头部增加“完整调用记录”入口，使用返回的 Run.RequestID 跳转到既有请求详情弹窗。
2. 历史详情元数据展示 `analysis_status`，让历史分析缺失/不可用状态可见。
3. 复用现有请求详情 API 与 UI，不增加新的数据查询链路。

## 4. 验收标准

- 历史 Benchmark 详情能显示关联请求 ID 和分析状态。
- 存在 RequestID 时可直接打开对应请求详情；请求详情使用现有 API 返回完整 RequestAnalysis 或明确不可用状态。
- RequestID 缺失时不显示无效入口。
- 不新增数据表、API、存储副本或第二套关联逻辑。
- Browser Smoke 与 Go CI 通过。

## 5. 验收结果

- 历史 Benchmark 详情入口已使用 Run.RequestID 跳转到现有完整请求详情。
- 历史详情已展示 analysis_status；缺少 RequestID 时不展示无效入口。
- Browser Smoke：Run `45133268206`，SHA `32ce0902487efd20909a872188c069e29ef03924`，success。
- Go CI：Run `45133513574`，SHA `b79f64967ef4bc8a843af9c0f40b6e0fd5b75deb`，6/6 Job success。
- Browser Smoke SHA 到最终 Go CI SHA 之间的 GitHub compare 仅包含 `.github/workflows/go-ci.yml` 与 PERF-13 文档变更，未改变 PERF-11 前端实现。
- 不新增关联表、API、存储副本或第二套关联逻辑。

## 6. 后续边界

PERF-11 已完成。PERF-13 负责验证观测链路的代码路径成本与真实部署运行分布；PERF-12 的四组 observed 样本不因本任务改变资格。
