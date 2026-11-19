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


## 后续补充：自然工具决策与强制调用分离（2026-11-19）

原始 BENCH-05 的 \`tool_performance\` 场景使用固定 \`tool_choice\` 指定 \`web.search\`，并在默认 Prompt 中要求必须调用工具。该实现用于验证强制调用链路，但不能衡量模型自主判断工具使用的能力。

后续兼容性修正：
- 新增 \`tool_performance_auto\` 自然决策场景，默认 \`tool_choice=auto\`，工具定义仍由服务端 Registry 提供，模型自行判断是否调用。
- 保留 \`tool_performance\` 原强制调用语义，作为独立诊断模式；既有历史任务和 API 调用不重新解释。
- 新增工具决策结果、执行尝试/成功/失败数量和执行成功率。自然模式成功完成但未调用工具时，工具执行阶段为 \`not_applicable\`，不是 0ms 或 unknown。
- 自然决策和强制调用的场景及配置指纹不同，禁止跨模式严格比较。
- 管理台已提供两个入口，默认测试问题不包含“请调用工具”等指令。

本补充不改变 Coordinator、Runtime、工具权限或普通推理主流程；代码与自动化测试结果以当前任务文档后续记录为准。


## 真实模型行为验收（2026-11-19）

> 状态：待部署环境执行。代码与 CI 通过不代表真实模型已完成五类行为验收。

### 验收前置条件

- 在目标 Gateway 管理台选择实际要验收的模型；记录模型 ID、Provider/Backend、部署版本和测试时间。
- Gateway 配置中启用工具系统，并将 `web.search` 加入允许工具；SearXNG 地址可用。
- 本次使用的 Web 服务 API Key 必须具有 `web.search` 的 `allowed_tools` 授权，并允许当前模型；管理台登录 Session 与 Web 服务 API Key 是两种不同凭据。
- 先确认 SearXNG 正常，再执行成功路径。每个场景单独创建任务，避免多个任务相互覆盖；Benchmark 当前最多允许一个活动任务。
- 使用低输出上限（建议 256～512 Token）控制本地模型耗时。不要为了制造失败而修改生产 SearXNG 配置。

### 五类行为矩阵

| 编号 | 场景 | 建议输入 | 必须核对的证据 | 通过条件 |
|---|---|---|---|---|
| TM-01 | 自然决策并调用 | “Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。” | `tool_choice_mode=auto`、决策、事件数、事件状态、模型轮数、最终生成状态 | 决策为 `called`；至少一条工具事件；工具执行成功；有最终回答 |
| TM-02 | 自然决策不调用 | “请计算 17×23，并只给出结果。” | `tool_choice_mode=auto`、`tool_call_decision`、`tool_call_event_count`、工具阶段状态 | 任务完成；决策为 `not_called`；事件数为 0；执行阶段为 `not_applicable`；最终生成状态为 `combined_with_decision` |
| TM-03 | 工具执行失败 | 在隔离验收环境使用 TM-01 的输入；该环境的 SearXNG 指向专用、可控的故障响应端点（例如稳定返回 HTTP 503），再执行自然决策 | 决策、事件数、事件 `status/error_code`、执行尝试/失败数、任务状态 | 模型确实发起调用；事件明确记录 `failed` 或 `timed_out`；决策仍为 `called`，不能误报 `not_called`。任务失败可接受，不能要求失败工具后仍生成最终回答 |
| TM-04 | 工具结果回传并生成最终回答 | SearXNG 恢复正常后，使用 TM-01 的时效性问题 | 模型轮数、工具轮数、成功事件、工具执行耗时、最终生成耗时、最终回答 | 至少两轮模型调用；工具执行成功；工具结果回传后模型生成最终回答；最终生成状态为 `measured` |
| TM-05 | 强制调用诊断 | 选择“强制工具调用诊断”入口，输入“请搜索 Go 官方发布信息，并总结最新稳定版本。” | 场景 `tool_performance`、`tool_choice_mode=required`、决策、事件及执行结果 | 请求条件记录为 `required`；模型产生工具调用；工具成功执行并返回最终回答。若模型未按协议返回工具调用，记录为兼容性失败，不修改测试标准 |

### 执行与判读规则

1. TM-01、TM-02、TM-04、TM-05 在正常 SearXNG 环境执行；TM-03 只能在独立验收环境执行，使用专用故障响应端点。不要让故障配置影响生产流量。
2. 每次只运行一个 Benchmark 任务。自然决策场景必须使用“自然工具调用测试”入口；强制诊断必须使用独立的“强制工具调用诊断”入口。
3. `called` 只表示 Coordinator 观察到模型发起工具调用（包括被拒绝的调用），不表示工具执行成功。执行成功/失败必须以事件状态和执行统计为准。
4. `not_called` 只接受成功完成的自然决策任务及请求级决策证据；查询到零条审计事件本身不是充分证据。
5. 审计事件数量与请求级 `tool_call_event_count` 不一致、事件查询失败或历史证据不足时，执行统计应为 `unknown`，不得按零次执行处理。
6. TM-02 的决策耗时和最终生成耗时是同一次模型响应，状态为 `combined_with_decision`，不能相加。
7. 保存每次任务的 Job ID、模型/Backend、场景、任务状态、决策、请求级事件数、审计事件状态、模型轮数、工具轮数、执行成功/失败数及阶段状态。不得保存或分享 API Key、Authorization、Cookie、SearXNG 原始响应或不必要的请求正文。

### 当前自动化基线与未完成项

- 工具决策请求级证据、事件完整性校验、历史持久化和 MariaDB 增量迁移已通过自动化测试。
- 代码 SHA `c8c162f3b61802e1f3b43b0ce5bc799272fbbac6` 的 Go CI Run `44465704821` 全部 5 个 Job 成功；后续文档 SHA `6bb2da6a94e133bb4ff92e6977134c2ae649e658` 的 CI Run `44465984791` 也已成功。
- TM-01～TM-05 尚无真实部署任务记录。必须逐项取得实际模型任务详情后，才能更新为通过；模拟测试、Browser Smoke 和 CI 不能代替。
