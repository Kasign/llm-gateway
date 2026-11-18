# BENCH-05：工具调用性能测试

> 状态：已完成（代码与自动化验收）
> 更新日期：2026-11-18
> 仓库：Kasign/llm-gateway
> 分支：dev
> 完成基线：7fe516054ee9684e12a0c0259aa48f76f2df52af
> 后续任务：[BENCH-06 UI 与历史分析](../../当前任务.md)

## 1. 目标与范围

为 Benchmark 增加真实非流式工具调用性能场景，分别测量模型决策、工具执行、模型最终生成和端到端耗时；不改变普通 /v1 请求、工具权限或执行逻辑。

## 2. 实现内容

- POST /api/benchmark 支持 scenario=tool_performance；默认 scenario=performance，兼容原请求。
- 工具场景固定要求 web.search，使用非流式 Chat Completions 和 tool_choice 指定函数；工具定义由服务端 Registry 提供，不信任客户端定义。
- 创建任务前检查工具系统、Gateway web.search 配置、Key 工具 ACL 和模型 ACL；Worker 仍经过既有 core.Pipeline、Provider、Coordinator、Runtime 与请求级 Guard。
- 未改普通 /v1/chat/completions 主流程；普通 performance 场景继续使用流式请求。
- 通过仅存在于 Benchmark Worker Context 的回调记录 bufferedModelCaller 每轮模型调用墙钟耗时；普通请求没有该 Context，不增加计时开销。
- 每轮模型耗时以 JSON 保存到 benchmark_runs.model_round_durations_json；工具耗时继续复用 request_tool_call_events，不复制工具审计。
- Benchmark 状态 API 同时返回 model_round_durations_ms、tool_call_events 和 tool_metrics。
- OpenAPI 与初始/增量 MariaDB Schema 已同步。

## 3. 指标口径

| 指标 | 来源与定义 |
|---|---|
| model_decision_ms | 第一轮模型调用耗时 |
| tool_execution_total_ms | 关联 Request ID 的工具事件 duration_ms 之和 |
| model_final_generation_ms | 仅在任务 completed 且至少两轮模型调用时，取最后一轮模型调用耗时 |
| model_round_durations_ms | 每轮模型调用耗时，按调用顺序保留 |
| model_rounds | 模型调用轮数 |
| tool_rounds | 工具事件中的不同 round 数 |
| tool_calls | 工具事件数 |
| inference_latency_ms | 一次 Pipeline.Run 的端到端墙钟耗时，不等同于模型 Decode 时间 |

缺失阶段使用 unknown；普通性能场景使用 not_applicable。未执行的最终生成不会伪造为 0。工具事件的参数和结果正文不进入 Benchmark 指标对象。

## 4. 回归与验证

- 增加非流式、强制 web.search 请求构造测试，并确认普通性能请求仍为流式。
- 增加模型轮次耗时顺序与 0ms 有效测量测试。
- 增加决策/工具/最终生成指标汇总及缺失阶段状态测试。
- MariaDB 集成测试覆盖 model_round_durations_json 创建、更新和读取。
- OpenAPI 高风险契约检查通过。
- Go CI：run 44226176740，5/5 Job 成功（单元/集成、竞态、静态检查、MariaDB 审计负载、故障注入、Docker、配置/API 契约）。
- Browser Smoke：run 44226176764，成功。

## 5. 明确未包含

- Web 管理台尚未提供工具性能场景入口和历史任务列表/比较；归入 BENCH-06。
- 本任务未进行真实生产环境外部工具压测。
- TOOL-09 真实部署验收仍未完成：授权成功/未授权 403、普通请求旁路、SearXNG 故障与恢复、Request ID 关联和审计脱敏的部署证据，按用户决定留待后续统一回归。不得将本任务 CI 通过当作 TOOL-09 真实部署证据。

## 6. 后续

进入 BENCH-06：完成工具性能场景 UI、任务历史列表、执行明细、条件可比性和历史对照；前端只展示服务端指标，不自行计算性能数据。
