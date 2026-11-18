# BENCH-06：UI 与历史分析

> 状态：已完成（代码与自动化验收）
> 更新日期：2026-11-18
> 仓库：Kasign/llm-gateway
> 分支：dev
> 验收基线：f85d1cad09396846e53f82c0835f292cd862be85
> 后续任务：[PERF-01 现有字段与 timings 盘点](../../当前任务.md)

## 1. 目标

完成普通性能与工具调用性能的统一 Benchmark 管理入口、任务历史、详情和严格比较。工具功能测试仍保持独立的一次性协议；不改普通 `/v1/chat/completions` 主流程。

## 2. 实现内容

- 在模型测试页增加工具调用性能入口与专用提示词，调用现有 `POST /api/benchmark`，设置 `scenario=tool_performance`；工具定义由服务端 Registry 提供，服务端强制非流式 `web.search`，沿用现有模型 ACL、Key 工具 ACL、Coordinator、Runtime 与审计链路。
- 保留原有独立“工具测试”入口，不把一次性功能测试混入 Benchmark Task。
- 新增 `GET /api/benchmark` 历史任务分页 API：默认 20 条、最大 100 条，支持场景/状态筛选和 offset；管理员 Session 鉴权。
- 历史列表只返回任务摘要、耗时和条件完整性元数据，不返回 API Key ID、Prompt 正文、响应正文或工具参数。
- 历史详情复用既有任务状态 API 和 Request ID 关联数据；工具场景展示模型决策、工具执行总耗时、最终生成、轮数/调用数及脱敏工具事件。
- 新增 `GET /api/benchmark/compare?left=...&right=...`；服务端只在两任务均完成、场景一致、条件快照完整且指纹一致时认定严格可比。不可比时返回原因，不计算差值；可比时由服务端计算指标差值，方向为右侧减左侧。
- 前端只呈现服务端指标与比较结论，不重新计算性能数据；unknown 与未测值不填成 0。
- 历史详情打开不覆盖当前活动任务的恢复指针；页面导航与服务端任务生命周期保持解耦。

## 3. 数据与安全边界

- Task/Run 继续使用 MariaDB 持久化；模型轮次耗时与工具审计仍分别复用现有字段/事件表，不复制 Prompt、Answer、Reasoning 或工具 arguments/结果正文。
- 历史列表和比较接口均使用管理员 Session；响应设置 `Cache-Control: no-store`。
- `latency_ms` 保留 nullable 语义，合法的 0ms 测量与未知值明确区分。
- 严格比较依赖 BENCH-04 的条件指纹；partial/unknown 条件不作严格比较。

## 4. 验证

- Go CI：run **44233346596**，5/5 Job 成功，覆盖编译/Docker、单元与集成测试、竞态/静态检查、故障注入、MariaDB 审计负载、API 契约。
- Web UI Browser Smoke：run **44233346551** 成功，桌面、窄屏和核心交互验收通过。
- Browser Smoke 覆盖工具性能入口、无客户端 Key 时的本地拒绝、历史列表和比较区域存在性。
- 未进行真实生产环境外部工具压测；TOOL-09 真实部署授权/失败/旁路与审计脱敏证据仍按用户决定留待后续统一回归，不能以本任务 CI 替代。

## 5. 验收结论

BENCH-06 的代码与自动化验收完成，Benchmark 通用体系 BENCH-01 至 BENCH-06 完成。下一阶段进入 PERF-01，先盘点现有字段、计时起止点和可用的 llama.cpp timings，再确定需要补充的真实测量数据。

## 6. 后续缺陷修复：Benchmark 历史任务归属与详情展示

> 修复验收日期：2026-11-18  
> 修复基线：`bf6bb711de6de87a12e73a5d6e4c1ac59886961d`

BENCH-06 首轮验收后，发现两个 UI 缺陷：

1. `benchHistoryCard` 位于性能测试与工具测试模式容器之外，导致两个 Tab 都显示 Benchmark 历史任务，功能归属不正确。
2. 历史详情通过 `renderBenchResult` 写入当前性能测试输出区。工具测试模式下该区域不可见；在性能测试模式下又会覆盖当前测试结果，详情展示与活动任务状态相互干扰。

修复内容：

- 将历史任务列表收敛到性能测试模式，工具测试模式不再显示该区域。
- 增加独立的历史详情容器，详情展示不依赖当前性能测试面板的可见状态。
- 历史详情不再写入或覆盖当前性能测试输出。
- 增加详情收起交互，并展示任务摘要、性能指标、测试条件、阶段耗时及可安全展示的结果内容。
- Browser Smoke 增加两个模式的可见性断言、历史详情点击与正文断言，以及当前输出不被覆盖的隔离断言。

验收证据：

- Go CI：run `44307428346`，成功。
- Web UI Browser Smoke：run `44307428368`，成功；真实 Gateway 启动、前端 Bundle 构建、Chromium/Playwright 浏览器验收均完成。
- Browser Smoke 报告：`192/192` 项检查通过，失败项为 0；其中包括“工具测试模式不显示 Benchmark 历史任务”“性能测试模式显示唯一的 Benchmark 历史任务区域”“点击历史任务详情后在历史区域展示任务内容”“查看历史详情不会覆盖当前性能测试输出”。
- 验收产物：Actions Artifact `web-ui-browser-smoke`，包含 JSON 报告、桌面/窄屏截图及 Gateway 日志。

本次是 BENCH-06 已完成能力的缺陷修复与回归，不新增 Benchmark API 或数据模型，不改变 BENCH-06 的任务范围。Docker 默认 release 构建会重新生成前端 Bundle；`release-with-bundled-web` 使用仓库 Bundle 的发布路径仍需确保在发布前重新构建并提交对应产物，不能仅凭默认 release 的 CI 结果推断该变体已更新。

