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


### 真实模型验收采集脚本（2026-11-19）

- 新增 `scripts/benchmark_tool_acceptance.sh`，复用现有 `POST /api/benchmark` 与 `GET /api/benchmark/{job_id}`，不新增服务端接口或平行执行链路。
- 脚本每次只创建一个任务，轮询到终态后保存完整任务响应 JSON。设置 `ACCEPTANCE_CASE_ID` 时，脚本先读取服务端用例目录，校验用例存在、场景匹配且不是 `manual_only`，随后把用例 ID 随创建请求提交；终端摘要展示服务端 `acceptance` 判定，不在脚本中复制判定规则。未设置用例 ID 时保留原始采集模式。结果文件不包含请求认证头，但可能包含 Prompt 和模型输出；分享前必须人工检查并按需脱敏。
- 认证明确分离：`GATEWAY_ADMIN_SESSION` 是网页登录会话 Token，放在 `Authorization: Bearer`；`GATEWAY_CLIENT_API_KEY` 是具有模型及 `web.search` 授权的客户端服务 Key，放在 `X-Client-API-Key`。二者不可互换。脚本不会把凭据写入证据文件。
- 运行依赖 Bash、curl、jq。脚本按普通文件提交，使用 `bash` 执行。示例：
  ```bash
  export GATEWAY_URL="https://你的Gateway地址"
  export GATEWAY_ADMIN_SESSION="网页登录会话Token"
  export GATEWAY_CLIENT_API_KEY="客户端服务API Key"

  # TM-01：自然决策并调用（服务端自动给出验收判定）
  ACCEPTANCE_CASE_ID=TM-01 SCENARIO=tool_performance_auto bash scripts/benchmark_tool_acceptance.sh

  # TM-02：自然决策不调用（服务端判定）
  ACCEPTANCE_CASE_ID=TM-02 SCENARIO=tool_performance_auto \
    PROMPT="请计算 17×23，并只给出结果。" \
    bash scripts/benchmark_tool_acceptance.sh

  # TM-04：工具结果回传后生成最终回答（服务端判定）
  ACCEPTANCE_CASE_ID=TM-04 SCENARIO=tool_performance_auto \
    PROMPT="Go 语言当前最新正式稳定版的版本号和发布日期是什么？请说明依据并给出来源链接。" \
    bash scripts/benchmark_tool_acceptance.sh

  # TM-05：强制调用诊断并由服务端判定
  ACCEPTANCE_CASE_ID=TM-05 SCENARIO=tool_performance bash scripts/benchmark_tool_acceptance.sh
  ```
- 可通过 `MODEL_ID` 固定上游模型 ID；未设置时使用当前活动模型。默认最大输出 384 Token，默认轮询超时 1800 秒，可通过 `MAX_TOKENS`、`POLL_INTERVAL_SECONDS`、`POLL_TIMEOUT_SECONDS` 调整。输出目录默认为当前目录下 `benchmark-tool-evidence/`，文件权限受 `umask 077` 保护。
- TM-03 默认在服务端创建接口返回 HTTP 409，因为 `benchmark.isolated_environment` 默认 false。只有独立验收 Gateway 在 `config.yaml` 显式设为 true 并重启后，目录才会将 TM-03 标记为可执行；生产环境必须保持 false。仅修改 SearXNG 故障端点不构成隔离授权。脚本不会修改服务端工具配置，也不会触碰生产环境。超时退出时脚本报告 Job ID 和查询地址，不会伪造终态结果。
- 当前仅完成采集工具和使用说明，未访问或调用用户私有 Gateway；TM-01～TM-05 的真实模型结果仍全部待部署环境执行。


## 项目内正式验收能力建设（2026-11-19）

### 现状复审

本轮回读当前 `dev` 的 Benchmark 后端、管理台和历史接口，确认以下能力已经属于项目本身，不能重复开发：

- `POST /api/benchmark` 创建异步任务，`GET /api/benchmark/{job_id}` 查询结果，`DELETE` 取消任务。
- 管理台已有自然决策与强制调用两个入口；使用现有客户端服务 API Key 和管理员 Session 分离的认证方式。
- 管理台已有历史任务分页、详情、工具阶段指标、审计事件摘要和严格条件比较。
- 服务端已持久化请求级决策与事件数量；工具执行统计只在审计事件数量完整时计算。
- Shell 脚本只是调用上述 API 的命令行采集入口，不是另一套执行系统。

### 真正缺失的项目能力

当前管理台能启动单次 Benchmark 并展示指标，但没有把 TM-01～TM-05 定义成项目内可识别的验收用例，也没有服务端统一生成的验收判定结果。用户仍需手工对照文档判断通过、失败或证据不足。脚本同样只采集响应，不生成正式判定。

因此后续工作不应新建 Benchmark 执行 API、数据库任务体系或平行 UI，而应在现有 Benchmark 链路上补齐：

1. **用例目录**：项目内维护 TM-01～TM-05 的稳定 ID、目标场景、前置条件、成功条件及风险级别；TM-03 明确标记为仅限隔离环境。
2. **任务关联**：创建 Benchmark 时可携带验收用例 ID；将该 ID 写入现有任务配置快照，避免新增重复任务表。
3. **服务端判定器**：基于持久化任务状态、请求级决策、审计完整性、工具执行结果、模型轮数及最终生成阶段，输出 `pass` / `fail` / `inconclusive` / `not_run`。不得让前端或 Shell 脚本自行计算。
4. **历史可追溯**：历史详情和比较接口展示用例 ID、判定结果、判定原因及判定规则版本；旧任务没有用例 ID 时保持未关联，不回填猜测。
5. **安全隔离**：TM-03 在服务端拒绝非隔离环境执行；隔离环境标识必须来自可信服务端部署配置，不能接受客户端自报字段。当前若没有可信环境标识配置，先只提供人工隔离执行说明，不开放自动执行。
6. **统一采集器**：保留 Shell 脚本作为 API 客户端，调用项目内用例定义和判定结果；不能复制判定规则。

### 实施顺序

- A：梳理任务配置快照、历史详情恢复、OpenAPI 和管理台表单的现有接线，确定最小兼容字段。
- B：实现用例目录与纯函数判定器，先覆盖五类用例的边界测试。
- C：将用例 ID 纳入现有 Benchmark 创建和持久化配置快照，历史详情复用同一判定器。
- D：管理台增加用例选择与判定展示，继续复用现有历史列表和详情区域。
- E：更新 OpenAPI、脚本与文档，运行 Go CI 和 Browser Smoke。
- F：部署环境逐项执行真实模型验收；TM-03 仅在确认隔离环境后执行。

项目化能力已进入实现阶段，当前代码状态与待验收边界见下方“项目内验收能力第一阶段实现”。五类真实模型验收仍全部待部署环境执行。

### 项目内验收能力第一阶段实现（2026-11-19）

- 新增服务端 TM-01～TM-05 用例目录，管理员通过 `GET /api/benchmark/acceptance-cases` 读取；每项定义稳定 ID、标题、描述、Benchmark 场景、工具选择模式和是否仅限人工/隔离环境。
- `POST /api/benchmark` 接受可选 `acceptance_case_id`。服务端校验用例 ID 与场景匹配，并将 ID 写入现有 `benchmark_tasks.config_json` 配置快照；没有新增表或重复执行链路。旧任务配置中没有该字段时保持未关联。
- 任务详情由服务端判定器返回 `acceptance`，包含 `pass / fail / inconclusive / not_run`、稳定原因码、说明和规则版本。实时任务与 Gateway 重启后从 MariaDB 恢复的历史任务复用同一判定器。
- 判定只使用服务端任务状态、请求级决策、审计完整性、工具执行事件、模型轮数和最终生成阶段；前端与 Shell 脚本不计算结论。审计不完整或关键证据缺失返回 `inconclusive`，不能按零次调用推断成功或失败。
- TM-03 判定要求审计事件明确为 `failed` 或 `timed_out`；`cancelled` 不计为工具故障。服务端以部署配置 `benchmark.isolated_environment` 控制是否开放创建，默认 false；未启用时返回 409，管理台目录将其标记为不可自动执行。客户端不能通过自报环境字段绕过。
- 管理台已增加用例选择、默认提示词切换、执行入口、当前任务判定展示、历史列表用例 ID 和历史详情判定说明；仍复用现有 Benchmark 页面与历史数据。
- `scripts/benchmark_tool_acceptance.sh` 增加可选 `ACCEPTANCE_CASE_ID`，先读取服务端目录校验场景和 `manual_only`，再创建任务；结果摘要输出服务端 `acceptance`。未设置用例 ID 时保留原有普通采集行为。
- OpenAPI 已补充用例目录、创建请求字段、任务字段、判定结果和 TM-03 的隔离错误响应。
- 自动化测试覆盖五个用例的成功路径、决策缺失、审计不完整、任务未结束、场景不匹配、TM-04 缺少第二轮、TM-03 取消事件不能冒充故障，以及配置快照恢复。
- 本阶段没有访问用户私有 Gateway，也没有执行真实模型请求。TM-01～TM-05 的真实模型部署验收仍待执行；TM-03 的服务端隔离配置开关已加入，尚需在专用验收 Gateway 验证启用路径，生产环境保持关闭。

### 当前验证状态

- 最新代码提交：`059790429a77b5f0410da2eea0ad6890ed08423d`。Go CI Run [44473562385](https://github.com/Kasign/llm-gateway/actions/runs/44473562385) 最终 `success`，5/5 Job 全部通过；MariaDB 迁移集成、完整 Go 测试、竞态测试、静态检查、API 契约、前端、Docker、故障注入和稳态审计负载均通过。
- Browser Smoke Run [44473562450](https://github.com/Kasign/llm-gateway/actions/runs/44473562450) 对同一 SHA 完成，桌面、窄屏和核心交互验收成功。
- `benchmark.isolated_environment` 默认关闭、服务端目录动态标记和 TM-03 创建门禁已通过当前 SHA 的 Go CI；实际专用验收 Gateway 启用后的端到端验证仍未执行。
