# PERF-14 性能瓶颈归因与优化建议

> 状态：已完成
> 更新日期：2026-11-23
> 仓库：Kasign/llm-gateway
> 分支：dev
> 开始基线：`b7ff0e1493e0852d2547606bdea9a651c8b8f2cf`
> 任务规划：[整体任务规划](整体任务规划.md)
> 总体方案：[本地模型全链路性能分析方案](本地模型全链路性能分析方案.md)

## 1. 目标

把现有性能采集数据转化为可解释、可追溯、可执行的性能诊断结论，回答：

1. 这次请求慢在哪里？
2. 结论由哪些原始指标和时间边界支持？
3. 当前证据足以确认瓶颈，还是只能列为候选？
4. 哪些优化动作值得尝试，预期影响是什么，哪些动作目前不应尝试？
5. 优化后如何用相同条件 Benchmark 验证是否真的改善？

本任务先完成请求级证据归因和管理台展示，不自动修改运行参数、不自动切换模型、不自动重启服务。

## 2. 现状审查结论

### 2.1 已有能力，必须复用

- `RequestAnalysis` 已保存阶段耗时、TTFT、吞吐来源、llama.cpp 原生 Prompt/Decode timings、Request Timeline 和 Runtime Snapshot。
- `request_logs.analysis_json` 已作为分析数据的唯一持久化位置；请求详情 API 已提供 persisted / reconstructed / unavailable 状态。
- Benchmark Run 通过唯一 `RequestID` 关联完整请求分析；历史 Benchmark 详情已有完整请求详情入口。
- llama.cpp Metrics 后台采样和 Runtime Snapshot 已有独立采集路径，不应把主动探测放入普通推理热路径。
- Benchmark 已有条件快照、条件指纹、严格/有限可比等级和串行重复采样报告。
- 现有 `buildRequestDiagnosis` 主要根据错误码、错误文案和请求形态给出提示，不是性能瓶颈归因引擎。
- 现有 `summarizeAnalysis` 主要拼接耗时与吞吐摘要；PERF-14 已新增 `performance_diagnosis` 数据契约，当前仅支持 observed_only / insufficient_data 和候选方向，不确认主瓶颈。
- 当前没有独立 `internal/performance` 包；本任务先基于现有职责决定最小实现落点，不为了目录图搬迁代码。

### 2.2 当前数据不足

- PERF-12 四组样本保留为 observed，但因首组测试前模型加载状态 unknown，不能作为正式四组基线；不得把它们包装成已通过的基线。
- PERF-13 两批 NUC 串行采样均已完成，第二批 10/10 limited 可比，但输入/输出均为 13/10 且部分缓存命中，只能描述该短请求场景，不能代表长上下文或长生成性能。
- 当前 Runtime Snapshot 中的 CPU/内存主要是宿主机或时间点快照，不等于 Gateway 进程 RSS、容器独占内存，也不能直接归因到单个请求。
- Metrics Counter Delta 属于进程窗口证据；并发时不能分摊给窗口内的单个请求。
- 当前没有证据支持直接判定 NUC 的 CPU 线程数、batch/ubatch、上下文长度或 KV 配置就是瓶颈。

## 2.3 归因输入矩阵（首轮代码审查）

| 输入 | 真实来源与作用域 | 首轮使用方式 | 禁止推导 |
|---|---|---|---|
| `prep_ms` | Gateway Pipeline 的 Resolver/Mutator 等准备区间；请求级 | 作为 Gateway 推理前阶段观察项 | 不直接判断为慢；不与其他不重叠性未知的阶段重复相加 |
| `model_load_ms` | PreparedAt→ReadyAt；请求级，实际包含请求级/全局 Guard、Provider RPM 与就绪等待 | 展示为 Gateway 就绪等待观察项 | 不得解释为纯模型加载耗时 |
| `provider_queue_wait_ms` + status | Provider admission 回调；请求级 | 仅 status=granted 且等待值大于 0 时列为排查候选 | 单次正值不等于主瓶颈；不得与 upstream_ms 相加 |
| `prompt_ms` + `prompt_n` | llama.cpp response timings；请求级原生 Prompt Eval | 有正向 Eval Token 和原生耗时时列为候选 | 不用输入总 Token 替代 prompt_n；不混合不同缓存状态 |
| `predicted_ms` + `predicted_n` | llama.cpp response timings；请求级原生 Decode | 有正向输出 Token 和原生耗时时列为候选 | 不用 Gateway 流式窗口派生速度替代原生 Decode |
| `upstream_dispatched` → `upstream_headers` | Timeline 两个真实单调时钟锚点；请求级 | 仅两个事件均 measured 且顺序合法时计算区间 | 不把 request-start 到 headers 的偏移当成该区间；不等同模型 Prompt Eval |
| `upstream_headers` → `upstream_first_byte` | Timeline 两个真实单调时钟锚点；请求级 | 仅两个事件均 measured 且顺序合法时计算响应体首字节等待 | 不等同模型 Decode，也不单独区分上游计算与传输等待 |
| Runtime Snapshot | 请求收尾时最近后台样本；时间点/主机级 | 只展示状态、采样年龄和来源作为上下文 | 不等于请求区间资源曲线、Gateway 进程 RSS 或容器独占内存 |
| llama.cpp Metrics Counter Delta | 两次后台采样间的进程窗口；进程/model scope | 保留原始窗口、进程身份、series 与 not_comparable 原因 | 并发窗口不可分摊到单请求 |
| Benchmark 条件指纹 | Task ConfigJSON；模型 ID、Backend、Prompt SHA、Backend 版本、加载状态、硬件和推理参数等 | 后续同条件比较的必要条件；缺少追溯字段时只能 limited | 不跨模型、参数、场景或缓存类别合并 |

## 3. 归因数据契约

首轮请求级诊断的总状态为 `observed_only` 或 `insufficient_data`，单项排查方向标记为 `candidate`。`identified`、`not_comparable`、`no_bottleneck_detected` 需要 PERF-15 的同条件对照或明确 SLA 后才可产生；本轮不提前伪造这些结论。

后续完整归因状态需要区分：

- `identified`：存在直接、口径匹配且足以支持归因的证据。
- `candidate`：存在相关观测，但缺少同条件基线、资源对齐或关键锚点。
- `not_comparable`：指标来源/作用域/条件不匹配，不能比较。
- `insufficient_data`：所需数据缺失。
- `no_bottleneck_detected`：当前采样范围内没有发现达到判定条件的瓶颈；不等于系统不存在瓶颈。

归因结果至少包括：

- 瓶颈类别与状态；
- 受影响阶段及其准确时间口径；
- primary / secondary 候选（仅在证据足够时设置）；
- 证据项：字段、数值、来源、作用域、采样时间/窗口、可比等级；
- 结论限制及不能得出更强结论的原因；（当前不输出数值化置信度，避免没有校准数据时制造精确感。）
- 建议动作、适用前提、预期影响方向、风险和复测条件。

## 4. 归因规则与硬边界

1. Gateway 阶段耗时、Provider Queue、上游 headers/first byte/TTFT、llama.cpp Prompt/Decode timings 必须按既有时间边界解释，不得把时间偏移当阶段 duration。
2. Provider queue wait 是 upstream 阶段的子阶段，不得与 upstream_ms 相加。
3. `prompt_ms`、`predicted_ms` 和匹配 Token 数优先使用 llama.cpp 原生值；不得用 Gateway 流式窗口速度冒充 Decode 原生吞吐。
4. 没有同模型、同 Backend、同推理参数、同场景、同缓存分类的有效 Benchmark 对照时，只能报告 observed/candidate，不得输出 primary bottleneck。
5. 进程 Metrics 窗口不能在并发情况下归属单请求；Runtime Snapshot 只能描述采样时刻，不得伪装成请求区间资源曲线。
6. 不得仅凭 CPU 利用率高、内存占用高或单次 TPS 低就建议增加线程、调整 batch 或更换模型。
7. 缺失、未知、不适用、真实 0 必须区分；不以相邻指标填补缺失数据。
8. 不读取或展示 Prompt、Response、Reasoning 正文作为性能证据；诊断结果不包含 Secret。

## 5. 实施步骤

### 阶段 A：数据与代码审计

- [x] 确认 RequestAnalysis / RequestDiagnosis 当前职责和数据结构。
- [x] 确认 Benchmark、RequestID、Profile 持久化和历史详情复用关系。
- [x] 确认当前真实 NUC 样本的适用范围及不可推导结论。
- [x] 逐项审查 Timeline、Runtime Snapshot、Metrics 与 Benchmark 条件字段的可用性，形成归因输入矩阵。
- [x] 根据现有包职责确定最小实现落点：沿用 `internal/server` 的 RequestAnalysis 构建与持久化，不新建平行采集/分析服务。

### 阶段 B：请求级证据归因

- [x] 定义首版归因 DTO：状态、候选类别、证据项和下一步排查建议。
- [x] 实现保守候选规则：Gateway 推理前阶段、Provider Queue（granted/timeout/cancelled）、Prompt Eval、Decode、上游响应头等待、响应头到首字节等待。
- [x] 补充正例、反例、缺字段、未知队列状态、Timeline 锚点不完整、Runtime Snapshot 不可归属和历史 JSON 兼容测试。
- [x] 将归因结果复用现有 RequestAnalysis 持久化与详情读取路径，不新增重复存储。

### 阶段 C：管理台展示

- [x] 请求详情展示候选项、证据来源和结论限制；无证据时显示数据不足。
- [x] Benchmark 历史详情复用 RequestID 跳转完整请求详情，因此复用同一归因结果。
- [x] 无同条件基线时不输出主瓶颈，不显示猜测性参数调整结论。
- [x] 更新 OpenAPI RequestAnalysis 契约；Browser Smoke 待验收。

### 阶段 D：验收

- [x] Go 单元/集成/竞态测试与静态检查通过（CI `45178183405`，6/6 Job 成功）。
- [x] Browser Smoke 覆盖 observed_only / insufficient_data 与候选证据展示（CI `45178183474` 成功）。
- [x] 归因只在请求结束后构建，不改变普通 `/v1` 请求行为、认证、流式、取消和审计边界。
- [x] 代码、契约和 UI 回归测试最终提交：`d81b951697d190125f8e64f5048038f95bd2c8ec`。Go CI 与 Browser Smoke 均通过。

## 6. 下一阶段（PERF-15）预留边界

PERF-14 完成后，PERF-15 才建设优化实验闭环：

- 基于 PERF-14 归因结果生成受控优化实验；
- 记录优化前后参数快照、模型/Backend 身份和环境条件；
- 对同条件样本进行统计比较，明确缓存分组；
- 记录建议采纳/拒绝、实际变更和回滚方式；
- 复测后报告改善、无变化、退化或不可比较；
- 不自动修改生产配置，不把相关性描述成因果证明。

## 7. 当前进度

已完成现状审查、归因输入矩阵和首轮实现。代码提交 `511ae62698696da6f357840a0c3b12df1adbedc9`；最终代码提交 `d81b951697d190125f8e64f5048038f95bd2c8ec`；Go CI `45178183405` 6/6 Job 成功，Browser Smoke `45178183474` 成功。首轮请求级证据归因已完成，当前结论仍为候选排查方向，不宣称已经确认实际主瓶颈。
