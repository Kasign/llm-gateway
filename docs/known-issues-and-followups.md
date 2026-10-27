# LLM Gateway 未关闭问题与后续验收台账

> 更新日期：2026-10-26。此文件是问题统一入口。发现问题先登记、分级并定义验收条件，按优先级集中解决；关闭事项时附 commit、CI 运行与可复查结果。历史记录中的 PR 状态可能早于当前基线，当前状态以本节最新复核和后续增量记录为准。

## 当前基线

- 本轮配置治理开始前 `dev` HEAD：`f89d064bc0b31478079e71631cbe23a18db139af`；新配置契约门禁提交及其 Actions 为后续最新状态。
- PR #5：[Gateway TCP 连接失败与恢复](https://github.com/Kasign/llm-gateway/pull/5)，已于 2026-10-26 合并至 `dev`，合并提交 `6fcdc81e35bfc73b933aaa34a6c62538727bd0b0`。
- PR #5 最终 CI：[Actions #156](https://github.com/Kasign/llm-gateway/actions/runs/39939567499)，结论 success，26 个步骤全部成功；代码修复提交的 Actions #155 同样全量通过。
- PR #7（[远程 Provider 并发上限与多 Provider 管理](https://github.com/Kasign/llm-gateway/pull/7)）已于 2026-10-26 合并至 `dev`，合并提交 `17ce32918a872341a0475d103b69097b061acf53`。并发预算热更新顺序修正随 PR #7 一并合入；合并后 Actions [#40298422463](https://github.com/Kasign/llm-gateway/actions/runs/40298422463) 的负载验收失败，仍需统一回归。
- #155/#156 的数据源是本机 loopback Mock + CI Redis/MariaDB。不能据此宣称真实供应商 E2E、公网链路恢复或生产容量已经验收。

## P1：可靠性与可观测性

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-REL-001 | DNS NXDOMAIN/解析超时此前只覆盖 Provider 层错误映射；现已补齐 Gateway Handler + Redis + MariaDB 专项集成测试。 | Actions #163 全量验收通过 | 使用注入 Resolver 返回确定性 NXDOMAIN/timeout，验证 Gateway 安全响应与审计行的 Gateway 状态/NULL 上游状态；不依赖公网 DNS。 | NXDOMAIN=502、Resolver timeout=504；两条审计记录 `upstream_http_status IS NULL`（2/2）；不泄漏主机名、Resolver 诊断或凭据；Actions #163 全量 CI 通过。 |
| GW-REL-002 | 上游已提交 HTTP 200 和部分 SSE 后发生 TCP RST 的行为，原先缺少完整 MariaDB 集成测试。 | Actions #164 全量验收通过 | 在 Gateway Handler + Redis + MariaDB 集成测试中，上游发送 200 与部分 SSE 后触发 TCP RST；验证已提交状态不可逆及审计一致性。 | 客户端 HTTP=200、保留部分 SSE 并追加安全 `event: error`；审计 `http_status=200`、`upstream_http_status=200`、`status=error`、`error_code=provider_upstream`；无凭据/底层网络诊断泄漏；Actions #164 全量 CI 通过。真实供应商 E2E 仍未覆盖。 |
| GW-REL-003 | TCP 拒绝后同端口恢复已由 PR #5 覆盖并合入 `dev`。 | 已合入，合并后整体回归仍需持续关注 | 不再从旧分支重复合入；后续回归检查请求与审计状态的一致性。 | 新 `dev` Head 对应完整 Actions 通过，并在统一回归中保持恢复语义。 |
| GW-OBS-001 | 审计队列已有有界容量、关键任务预留及 dropped 计数。 | 实现完成；Actions #162（Head `dfbc2955`）全量通过 | 原子计数与低成本快照暴露队列深度、容量、成功/失败、按原因丢弃、累计排队时延/最大排队时延和累计 drop rate；管理员 GET API 不查询数据库，不返回正文、Key 或原始错误。 | Actions #162：单测、MariaDB 审计积压、Gateway 负载/故障注入/TCP 恢复、race、性能基线、go vet、Go 构建与前端 bundle 均通过。 |
| GW-OBS-002 | 请求日志 / Payload 的数据库保留期限与自动清理。 | 清理逻辑、MariaDB 集成用例和只读运行状态 API 均随完整 Actions #225 通过 | `logging.retention_days` 默认 0（禁用）；正数保留天数后，按 500 条默认批次、每轮最多 10 批清理已结束请求，先删除流事件和 Payload 再删除请求日志；单批独立事务并支持取消。运行状态只保留进程内聚合计数，API 不暴露原始数据库异常。 | 完整 Actions 通过；状态 API 可区分 waiting/success/failed/cancelled，返回耗时、删除量和失败次数，且不泄露原始 DB 错误。 |
| GW-OBS-003 | 管理台轮询对 DB/CPU/推理热路径的影响仍需实测；短期缓存不等于完整负载证明。 | 待基准 | 对照无轮询、常态轮询和高频轮询，测量 DB 查询量、延迟分位数、CPU 与缓存命中率。 | 同一 Go 版本和相近 Runner 下重复采样；定义预算与刷新频率；不凭单次抖动设置阈值。 |
| GW-NET-001 | TLS 握手中断、连接池陈旧连接及公网代理链路的覆盖不相同。 | TLS 握手中断、陈旧 Keep-Alive 恢复用例均随完整 Actions #222/#224 通过；公网反代/路由仍待部署环境验收 | 区分故障注入：TLS 在握手前关闭；陈旧连接先建立成功响应，再由服务端清空空闲连接，第二次请求必须成功并建立替代 TCP 连接。公网链路不能由 loopback 代替。 | 陈旧连接测试在完整 CI/race 通过，第二次请求成功且请求/连接计数满足预期；公网项另附环境、时间与脱敏日志。 |

| GW-NET-002 | Provider Transport 复用的 Keep-Alive 连接可能被 NAT、反向代理或上游空闲超时提前关闭。 | 陈旧连接恢复用例由完整 Actions #222/#224 复验通过；公网反代/真实供应商仍需部署环境确认 | 首次请求成功后关闭空闲连接；不得盲目重试可能已执行的 POST；允许当前请求映射安全 502，但下一次请求必须建立替代 TCP 连接并成功。 | 当前完整 Actions 的 race、静态检查、Go build 和 bundle 均通过；公网反代和真实供应商链路仍单独验收。 |

## P1：运行治理

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-GOV-001 | 远程 Provider 缺少按 Provider 独立配置的并发上限和一致的过载语义。 | Provider 并发、RPM 与热更新完整 Actions #222/#224 通过；真实供应商流量仍需部署环境验收 | `max_concurrent_requests` 默认/上限均为 64；过载不发起上游请求，`UpstreamStatus` 为空；流式已提交时追加安全 SSE error。热更新实例必须复用同一个 Provider 并发许可器，避免旧请求仍在执行时替换实例导致总并发超限。 | 新 `dev` Head 的完整 CI/race 通过；统一回归确认旧请求许可释放和负载持久化。 |
| GW-GOV-002 | Provider 级 RPM、每日 Token 预算与可选排队已接入，等待本次新 Head CI。 | RPM 与 Token 预算此前已由 Actions 验证；本提交增加可选的进程内有界 FIFO 队列及取消/超时/满队列语义，等待新 Head CI | `requests_per_minute=0` 默认关闭，正数按 UTC 固定分钟窗口计数。`token_budget_per_day=0` 默认关闭；启用后对同一 Provider 跨 API Key/Agent 共享 Redis UTC 日桶，原子预占并在可信供应商 usage 到达后结算。无可信 usage 时保守保留预占。 | 最新 Actions 通过预算边界、跨日隔离、真实 usage 结算和动态 Web 配置；等待队列默认关闭（`max_queue_size=0`），启用后为进程内 FIFO、有界 1024、支持 Context 取消及 503 超时/满队列错误；不会将其与 Redis RPM 或 Token 预算混为一套状态。 |
| GW-GOV-003 | 自动重试/故障转移尚未实现。 | 明确暂缓，非当前缺陷 | 定义幂等性、已提交响应判定、Provider Retry-After、预算与重复请求语义；SSE 开始输出后不得换 Provider 产生第二份答案。 | 经架构 Review 后独立决策；未批准前不启用自动切换。 |

## P2：外部兼容与产品增强

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-E2E-001 | OpenAI-compatible Chat、Responses 与 Anthropic Messages 的 opt-in 真实端点用例存在，但 CI 默认跳过，没有实际调用真实供应商。 | 待授权环境验收 | 在用户控制的安全环境提供 HTTPS BaseURL、Provider 专用 Key 和模型名；确认计费额度后执行短请求；禁止将 Key 写入仓库、日志与 Artifact。 | 记录供应商、模型、端点、日期、状态/协议核验结果与脱敏日志；开发 CI 默认不强制联网。 |
| GW-E2E-002 | 真实供应商各模型的工具调用、结构化输出、Reasoning、视觉、错误封套、真实限流与余额不足行为并未全部验证。 | 部分依赖供应商 | 按 Provider × API × 能力建立兼容矩阵，只验证有实际需求的组合。 | 每个矩阵单元注明通过、失败、不支持或未测；一家通过不代表所有厂商通过。 |
| GW-PROV-UI-001 | Web 多 Provider 管理与 `.env` 预置。 | 代码随 PR #7 合入 `dev`，完整 Actions #222/#224 通过；真实供应商与已部署 Web 行为仍需用户环境验收。 | 继续按统一测试安排验证配置来源、密钥存储、Registry 更新/回滚与活动 Provider 安全边界。 | 完整 CI 通过，并在部署环境核对密钥不回显、活动 Provider 无法被间接停用/清 Key、Registry 失败时配置回滚。 |
| GW-AGENT-001 | 同一 Agent 内多模型别名映射、Agent 级不同 Key/权限策略和配置热更新尚未实现。 | 待产品需求确认 | 保持 Agent 固定 Provider/默认模型的现有契约；需求明确之前不擅自增加权限与路由复杂性。 | 配置模型、优先级、热更新一致性和多 Agent 隔离均有测试。 |
| GW-API-001 | OpenAPI 的字段级 Schema 与 Go Handler 语义等价性仍需逐步补齐。 | 63 个字面路径模板、72 个 operation 的元数据门禁及 67 个运行时路由路径双向对账由完整 Actions #225 通过；仍需逐项补齐字段级 Schema 等价性。 | 继续在实际功能改动时对照 Handler 的请求体、成功状态码、错误响应和稳定 JSON 字段；不一次性声称全部契约完成。 | 路由与 operation 元数据门禁通过；稳定 Schema 有可复核的字段/状态码约束；OpenAPI 的 `partial` 状态仅在字段级覆盖实际完整后调整。 |

## 本轮实施规则

1. `GW-OBS-001`（Actions #162）、`GW-REL-001`（Actions #163）、`GW-REL-002`（Actions #164）和本地 TLS 握手中断（Actions #166）均已验收。下一项按独立功能组推进 `GW-PROV-UI-001`。真实供应商 E2E 需等安全凭据与预算条件明确后再做。
2. 每次开发前核实 HEAD、PR 和 CI；不要依据过期会话状态修改远程代码。
3. 按完整功能组提交中文 Conventional Commit；CI 失败必须查看日志并修复，不降低断言。
4. 真实 E2E 仅在供应商凭据与预算明确可用时执行；Mock CI 不能替代真实端点验收。
5. “已完成”必须说明验证层级：单测、Mock HTTP、Gateway + Redis/MariaDB 集成、真实供应商 E2E 或生产部署验收。


## 2026-10-23 执行记录：并发上限功能与基线依赖

- PR #6（基于旧 `dev` 的第一版）由 Actions #157 暴露编译依赖：旧 Core 没有 `UpstreamStatus` 字段。PR #6 已关闭、未合并；没有删除断言。
- 当前 PR #7 基于 PR #5 的已通过 Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232` 堆叠，最终功能 Head `dd54cf576ba4a80e7c5d1f2dd0bb3be95a776b4b` 在 [Actions #158](https://github.com/Kasign/llm-gateway/actions/runs/39943828510) 全量通过。
- 2026-10-26 基线复核：PR #5、PR #7 仍为 Open/未合并；PR #7 当前 Head `8858da0e2833d7ed7e2ae1293e24e561e141578f` 的 Actions #164（运行 ID `40258215101`）完整通过，涵盖 DNS、TLS 以外的 TCP 故障恢复、已提交 SSE 中断、审计积压、负载、race、静态检查、Go 构建与前端 bundle。
- `GW-OBS-001` 已经 Actions #162 全量通过：审计快照与管理员只读 API、MariaDB 审计积压、race、go vet、Go build、前端 bundle 检查均成功。
- `GW-REL-001` 已由 Actions #163 全量验收：NXDOMAIN 返回 502、解析超时返回 504，MariaDB 两条记录的 `upstream_http_status` 均为 NULL，响应与审计元数据均未泄漏主机名/诊断/凭据。
- `GW-REL-002` 由 Actions #164 全量验收通过：上游发出 200 和部分 SSE 后 TCP RST，客户端保留 HTTP 200 并追加安全 `event: error`，MariaDB 审计为 `http_status=200/upstream_http_status=200/status=error/error_code=provider_upstream`。
- `GW-NET-001` 本地 TLS 握手中断回归测试已由 Actions #166 通过：对端在 TLS 握手前关闭连接，错误映射为安全 502，`upstream_http_status` 保持为空；独立步骤与 Artifact 已保存。连接池陈旧连接、跨网络路由与公网代理链路仍未验收。
- `GW-PROV-UI-001` 已纳入计划并细化配置来源/安全规则，但尚未实施：环境托管项只读、Web 项动态持久化、ID 冲突拒绝覆盖、Web Key 加密与掩码不回显。


## 2026-10-26 Web 多 Provider 功能组（进行中）

- 已增加 Web Provider 管理 API 与 AES-GCM 密文持久化，不回显 Key；配置来源冲突拒绝覆盖；编辑空 Key 保留旧值，显式清除为单独动作；停用/删除活动 Provider 返回 409。
- 已加入多 Provider 环境前缀配置、模型 ID 映射和安全连接测试；`/v1/models` 返回活动 Provider 别名，实际推理走映射后的上游 ID。
- 尚未关闭：活动 Provider 的清 Key 和编辑禁用保护已发现遗漏，当前功能组会补齐并复跑 CI；持久化回滚与 Registry 并发边界仍需继续审查。前端 CRUD、独立 Provider bundle、DeepSeek/OpenAI 模型映射 Compose 透传均已提交。

- 边界补充：创建 Provider 时若当前活动上游仍指向该 ID，将返回 409；模型别名仅对显式映射项生效，未知模型保持原默认上游行为。环境变量示例提供可选 `MODEL_MAPPINGS_JSON`，Compose 对 DeepSeek/OpenAI 显式透传。

## 2026-10-26 最新复核（提交 393b14b 之后）

- 工作分支 Head 为 `393b14b45a0483e4ab0452acf4c8d283b2567ae5`；dev 仍为 `950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。PR #5（Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232`）和 PR #7 均为 Open / 未合并。
- Actions `40262917490` 对应 Head `393b14b`，所有步骤成功：单测、MariaDB 审计积压、60 秒 Gateway 负载、429/503、TCP 恢复、DNS、SSE 中断、TLS 握手中断、race、性能基线、go vet、Go build 及前端 bundle 校验。
- 当前审查发现活动 Provider 清 Key 与通过保存表单设置 `enabled=false` 两条路径未命中此前的删除/停用保护。修复与回归测试正在集中提交；修复后的完整 CI 尚未完成前，不关闭 Provider 管理安全验收。
- 历史 Actions `40262338624` 在 HTTP 请求全部成功的情况下审计行数仅 10556/12192；之后的 `40262766329`、`40262917490` 和最新基线 `40263338398` 已通过完整负载/审计检查。保留原计数断言，不降低标准。

## 2026-10-26 追加：Provider 热更新与并发预算

- 审查发现旧 Provider 已进入推理且管理台保存配置时，Registry 会切换到新 Provider 实例；若新旧实例各有独立并发许可器，在途请求和新请求会分开计数，短时间内可能超过配置的 Provider 并发上限。
- 本批通过共享许可器解决：Web Provider 配置编辑复用既有许可器，更新 `max_concurrent_requests` 时影响旧实例与新实例；降低上限不会取消在途请求，而是拒绝新请求直至活动数回落。若 Registry 更新失败，恢复原限制并回滚持久化设置。
- 新增 Provider 层跨实例并发回归测试和 Server 构造测试。首轮 Actions #40266563695 在编译阶段发现旧断言引用已移除字段（非生产逻辑失败）；修复提交 `6b7c839` 的 Actions #40266726738 已全量通过。
- 为防止 Provider Registry 更新失败时短暂放宽旧并发上限，降低限额在 Registry 更新前应用并可回滚，提高限额只在更新成功后应用；对禁用 Provider 的保存先保守保留现有运行时预算，重新启用时同步新上限。该顺序新增独立单元测试，待本次提交完整 CI 验收。

## 2026-10-26 追加：请求历史保留策略

- GW-OBS-002 已实现可选的后台保留策略：默认关闭；显式配置后按保留天数和批次清理已结束请求，并在程序关闭时停止任务。
- 当前仅完成代码与配置/文档；按统一测试安排没有运行验证。MariaDB 到期/未到期、运行中请求、分批、并发写入、取消与清理性能仍待统一测试，不应提前将此项标记为已验收。


## 2026-10-26 追加：自定义 Provider 环境变量透传

- 已为 DeepSeek/OpenAI 之外的 Provider 增加独立可选 `providers.env` 注入，解决 Compose 只显式传递两组 ID 的限制。
- 保留主 `.env` 的最小显式环境变量白名单，不把数据库管理员口令整份传给 Gateway。示例文件仅含注释，旧部署缺失此文件可继续使用；尚未运行 Compose 校验。


## 2026-10-26 追加：Provider 最大响应大小透传

- DeepSeek/OpenAI `.env` 示例和 Compose 容器环境均已传递 `MAX_RESPONSE_BYTES`，补齐部署变量与 Provider 环境解析器之间的配置链路。
- 尚未执行运行验证，后续统一测试时再检查 Compose 渲染结果与实际容器环境。


## 2026-10-26 追加：Provider 级请求速率限制

- 完成远程 Provider 独立 `requests_per_minute` 配置，支持 YAML、`LLM_GATEWAY_PROVIDER_<ID>_REQUESTS_PER_MINUTE` 与 Web 管理台；0 禁用、最大 100000。
- 仅限制新请求，按 Provider 跨 API Key/Agent 共享 Redis 预算；Redis 故障时 fail-closed。不会对本地慢速模型加 RPM，也不会中断在途生成或设置整体生成超时。
- 已新增自动化测试代码但未运行。Token 预算和等待队列未在本次顺带引入。


## 2026-10-26 追加：Provider RPM 视图配置补齐

- Web Provider 安全视图包含 `requests_per_minute`，部署 YAML 也正确映射该值，避免管理台无法显示/编辑现存配额。
- `config.example.yaml`、`providers.env.example` 和中文配置说明补充了 RPM 样例。

## 2026-10-26 追加：调用记录按 Provider 筛选

- `GET /api/requests?provider=<id>` 与模型、状态、Key 名筛选组合使用；Provider ID 去除首尾空格并按小写完整匹配，支持自定义 Provider ID。
- 查询缓存以 Provider 条件隔离；在途请求以 LiveRegistry 最新 Provider 身份完成合并后再过滤。历史空 Provider ID 不会被反推归属。
- 已添加 Provider ID 匹配回归测试代码并同步前端源码、部署 bundle、API/数据库契约、技术说明和实施计划。按当前统一测试安排未运行测试、构建、迁移或 CI；需要后续验证缓存隔离、MariaDB 索引/查询计划及 Resolver 归属更新场景。

## 2026-10-26 追加：请求统计和 Token 来源口径

- 增加管理员聚合接口计划：固定 24h/7d/30d 时间窗口，统计请求量、成功率、平均耗时、延迟分布与 Token 来源分组；时间条件限定在 `started_at` 索引范围。
- Token 来源区分上游 usage、llama.cpp timings 与来源未知。异步写入 `analysis_json` 可能造成短暂的 unknown_source 过渡态；旧记录未标记来源不应被猜测为实际计费 Token。
- 已编写实现和范围测试代码并同步管理台 Bundle 与技术文档；本轮未运行测试、构建、迁移或 CI。后续统一验证 MariaDB 的 `JSON_VALID/JSON_EXTRACT` 行为、状态统计语义、窗口边界、空数据与聚合性能。


## 2026-10-26 追加：OpenAPI 首版与当前远端状态

- 当前 PR #7 Head：`6de324abd0f570800a33ad03e1ed0af7c4a86f96`（`feat: 增加调用统计与 Token 来源分析`）；PR 仍为 Open，目标 `dev` 当前 Head 为 `950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。PR #5 仍为 Open，Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232`，Actions #156 success。
- PR #7 当前 Head 对应自动 Actions 运行 [#40284057729](https://github.com/Kasign/llm-gateway/actions/runs/40284057729) 在单元测试步骤失败，之后的集成、race、静态检查、构建和前端 bundle 步骤被跳过。该状态不是本轮重新运行测试产生的；后续统一测试时需要一起复核。
- 本轮未触发额外测试、构建或 Actions 重跑。已核对的慢模型配置能力已有实现：本地 `request_timeout_seconds=0` 不设 Client 整体期限，`warm_start` 提供 SSE 保活，OpenResty 示例将代理读写空闲超时设为 86400 秒；客户端自身期限仍由调用方控制。
- 新增 `docs/openapi.yaml` 首版。其覆盖范围是有意限定的，不意味着所有管理端点都已有机器可读契约。Web Provider 的活动项禁用/清 Key 保护已能在当前源码中看到；在最新完整 CI 通过前继续将它列为待验收项。


## 2026-10-26 追加：OpenAPI 管理凭据与调用方 Key 扩展

- OpenAPI 新增 API Key 列表/创建/编辑/删除/启用/禁用/轮换，以及管理登录、登出、当前会话查询和凭据更新。
- 文档明确创建/轮换时明文调用方 Key 只显示一次，后续列表只有元数据；管理会话 Token、`X-Admin-Key`、调用方 Gateway Key 与上游 Provider Key 是不同凭据域。
- 以上是当前源码契约盘点，不等于自动 schema 差异校验。尚未补齐模型下载、运行时、Benchmark、推理参数等路由；未手动运行测试或重跑 CI。


## 2026-10-26 追加：请求审计、运行时与 Benchmark 的 OpenAPI 契约

- 已添加请求列表/详情/清理、内存审计队列指标、模型列表、运行时概览/本地 load-unload 与异步 Benchmark 任务契约。
- 请求详情明确提示可能包含保存的上下文/正文与头部；清空请求日志声明为破坏性操作。动态运行时 JSON 使用扩展字段 Schema，避免暗示固定字段集。
- 机器可读契约仍为分阶段覆盖；尚未加入 API/Handler 自动差异校验。没有手动运行测试或重跑 CI。


## 2026-10-26 追加：慢模型 Benchmark 的零时限修正

- 修正 `runBenchmark` 对 `llama.request_timeout_seconds: 0` 的处理，避免将零秒传给 `context.WithTimeout` 导致任务立刻超时。
- 新增可选时限 Context helper，0 表示无整体 Deadline，但仍继承用户停止/父 Context 取消；正数时限保持既有行为。
- 已新增针对 0、正数和父 Context 取消传播的回归测试代码。未手动运行测试；推送后 GitHub Actions 由仓库配置自动触发，状态以最新 Head 对应运行记录为准。

## 2026-10-26 当前 CI 阻塞与分支整合计划（Actions #208）

- 重新核实的 PR #7 工作分支 Head：`bd41c549511c700df2b3ba44cb64ca9ef5f1b4f8`；PR #7 仍 Open，目标为 `dev`（`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`）。PR #5 的 TCP 恢复测试仍 Open、未合并，因此 PR #7 不能先于 PR #5 合并。
- 最新对应 CI 为 [Actions #208](https://github.com/Kasign/llm-gateway/actions/runs/40286112907)，结论 failure，失败于单元测试步骤；后续 MariaDB 审计积压、负载/故障注入、race、性能、静态检查、构建与前端 bundle 步骤均被跳过。这是自动触发的 CI 结果，本轮未手动执行测试。
- 阻塞 1：`internal/server/provider_runtime_test.go` 的 `core.State` 测试字面量将 `RouteProviderID`、`ServiceModel`、`ServiceProviderID` 放在顶层。它们属于嵌入的 `core.Request`；修正测试为 `State{Request: core.Request{...}}`，不改生产类型。
- 阻塞 2：`internal/audit/writer_mariadb_test.go` 直接创建的 `request_logs` 夹具漏掉 `provider_id`，且 `CREATE TABLE IF NOT EXISTS` 不会补齐复用测试库中的旧表。夹具应定义该字段，并用 `INFORMATION_SCHEMA.COLUMNS` 检查、必要时补列；保留原有 500 条持久化断言，不改生产迁移逻辑。
- 当前修复作为一个逻辑提交，连同本台账及实施计划一起更新；不手动运行测试，提交后以新 Head 对应的 GitHub Actions 统一验证，不沿用旧 Head 的成功结果。
- 分支策略：先处理并合入 PR #5；随后把 PR #7 对齐最新 `dev`，重新检查最终差异与完整 CI，再决定合并。旧的并发分支 `feat/provider-concurrency-limits` 以及 PR #1–#4 的已合并功能分支暂不删除，待确认内容已包含在目标分支、并发分支没有独有提交后再清理；不强推、不改写历史。默认分支 `main` 与开发基线 `dev` 的关系另行确认，不在本次变更中修改。

## 2026-10-26 追加：Actions #209 暴露的 Provider 管理台语法错误

- [Actions #209](https://github.com/Kasign/llm-gateway/actions/runs/40287645485) 对 Head `b36451e612d7e6a4bdf68535d33e9794604f0690` 的单测、MariaDB 审计积压、Gateway 并发与故障注入、TCP/DNS/SSE/TLS 故障、race、性能基线、go vet、Go 构建及管理台主入口构建均通过；最后的“校验前端语法与关键功能字段”失败。
- 失败点是 `web/js/providers-entry.js:60` 的表格 HTML 拼接字符串在 `actions` 之后少了结束引号，Node.js 报 `SyntaxError: Invalid or unexpected token`。这是管理台 Provider 模块的语法缺陷，不是 Go 或 MariaDB 问题。
- 修正后将源码原样复制到 `web/static/dist/providers-admin.js`。按 `web/package.json` 的 build 脚本约定，该部署文件就是源文件的直接副本；无需改动无关的 `app.js` bundle。
- 按统一回归安排，不手动启动 Node/Go 测试；提交的新 Head 由完整 GitHub Actions 验证。通过前仍视为待验收。

## 2026-10-26 追加：Actions #210 暴露的审计队列写放大

- [Actions #210](https://github.com/Kasign/llm-gateway/actions/runs/40288357563) 的单测和 MariaDB 审计积压测试通过；Gateway 负载请求本身全部成功且达到了 200 req/s，但审计验收发现仅 9,146/12,192 条 request_logs，Writer 累计丢弃约 35,000 个排队任务。失败日志显示大量 `normal_capacity_reserved` 与 `queue_full`，不是 HTTP 请求错误或数据库字段异常。
- 根因是一次已解析请求被多次重复写审计：`CreateRequest` 已写入 `model_requested` 和 `stream`，随后仍重复 `PatchRequestAfterParse`；Resolver 又将 Provider ID 与实际模型拆为两条关键 UPDATE。持续负载下这些重复任务放大了单 Worker 的数据库写入压力并压满有界队列。
- 本次改为：创建请求时一次写入解析字段；Resolver 实际 Provider 与上游模型合并为一条关键 UPDATE。生命周期仍保留 Create、Resolver 更新和 Finish 三个顺序边界；Finish 继续写入最终 Provider/model/状态与耗时，LiveRegistry 仍立即更新在途请求视图。负载测试新增 Provider 精确归属断言，不降低原有 12,192 行、成功状态与模型匹配断言。
- 当前修复待最新 Head 的自动 Actions 验收。未手动运行 Go 测试或本地负载测试；不会以调大队列、减少请求量或放宽断言作为修复。

## 2026-10-26 追加：Actions #211 暴露的主管理台 Bundle 陈旧

- [Actions #211](https://github.com/Kasign/llm-gateway/actions/runs/40289139466) 的 Go 单测、MariaDB 审计积压、Gateway 负载与审计持久化、429/503、TCP/DNS/SSE/TLS 故障注入、race、性能基线、go vet、Go 构建以及 Provider 独立 Bundle 语法/一致性检查均通过。
- 最终契约步骤仍失败，原因是 `web/static/dist/app.js` 与 CI 用当前 `web/js/entry.js` 构建出的 `/tmp/llm-gateway-admin.js` 内容不一致；差异定位在请求统计 UI 模块，属于主管理台部署产物未随源文件同步，而不是语法错误。
- 保留 CI 中的字节级 `cmp` 检查。根据该次 CI 输出的实际单一差异块同步 `app.js`，不跳过检查、不缩减功能，也不改负载断言。该产物修正后的最新 Head 仍需完整 Actions 验收。

## 2026-10-26 追加：统计 API 错误脱敏与 Provider OpenAPI 状态码

- 统计聚合查询失败时原实现把底层数据库错误直接放入 HTTP 500 的 `message`，可能泄漏表/列名或数据库驱动细节。本批改为服务端记录具体错误、对调用方返回稳定的 `database_error` 和通用中文提示，并增加回归测试确认诊断文本不进入响应正文。
- 核对实际 Handler 后修正 `docs/openapi.yaml`：`POST /api/admin/providers` 的成功状态为 201；`DELETE /api/admin/providers/{id}` 为 204 且无响应正文。没有更改业务状态码，也没有更改 Key 或 Provider 管理权限。
- 该批为 API 错误边界与契约同步，待当前 Head 的完整 Actions 完成后再推送，避免与当前回归重叠。

## 2026-10-26 追加：Provider 热更新的并发预算窗口

- Review 发现当前更新流程先持久化配置，再降低共享 `ConcurrencyLimiter`。这在两步之间留下一个短窗口：新配置尚未应用到 Registry 前，旧 Provider 仍能按较高旧上限接收新请求。
- 同时，纯 helper 对“编辑已停用 Provider”只覆盖提高上限情形，未覆盖降低上限；现有逻辑可能改动仍被旧在途请求共享的 limiter。
- 修复方式：降低上限前置到持久化之前，MariaDB 保存失败时回滚旧预算；提高上限仍延后到 Registry 替换成功后。编辑已停用 Provider 不改变当前运行时预算，重新启用时才根据已保存的新配置应用限额。
- 增加禁用 Provider 降低上限边界的回归测试，保留现有 Registry 失败回滚与活动 Provider 保护。代码与测试已写，待整组提交触发的完整 CI 验收；未手动运行测试。

## 2026-10-26 追加：扩大管理 API 的 OpenAPI 覆盖

- 根据 `Server.Handler` 路由注册和各 Handler 实现，新增模型安装/删除/兼容动作、HF 文件/搜索、下载查询/清理、Backend 查看/切换/批量卸载/上游列表、Bootstrap 快照、目录搜索、Ollama tags/Pull/模型操作、本地库存与推理参数 Schema/读写/重置等端点契约。
- 新增可复用请求/响应 Schema，明确 model_key、HF repo/filename、Ollama name/job_id、Backend、推理参数和下载清理字段；动态 upstream、运行时和 inference params 保留扩展对象，不作虚假字段承诺。
- 文档明确 `POST /api/models/install` 与 `POST /api/admin/ollama/pull` 是后台任务入口，进度通过 `GET /api/downloads` 查询；模型卸载只释放内存，模型删除会移除本地模型。
- 本批只扩展文档与机器可读契约，没有手动执行测试。契约依然标记 partial，尚未实现从路由/Handler 自动生成差异报告的 CI gate。

## 2026-10-26 追加：OpenAPI 第二轮精确性 Review

- 为 Agent 固定 Provider 路由补齐 Completions、Responses、Anthropic Messages 和 Messages count_tokens，与实际 Handler 的前缀路由范围保持一致。
- 对 Ollama Library tags 明确 tag/full_name/size/context/input 字段；对 available_backends 明确 id/label/kind/base_url/source/editable 及可选运行信息，不再只用无字段对象表达已知稳定返回内容。
- 修正 OpenAPI 3.1 时间字段的空值建模，并避免在与 Handler 兼容的推理参数 reset body 上使用过严的 additionalProperties=false。
- 高风险代码 Review 复核了请求生命周期结束状态、响应已提交后的状态保持、审计 FIFO/有界队列、管理员会话与 Provider 运行时预算；本轮未发现需要额外扩大代码变更范围的阻塞项。Actions #214 尚在执行，本批修正先暂存为候选提交，待该轮回归结束后再推送，避免两个完整 CI 重叠。

## 2026-10-26 追加：将 OpenAPI 路由覆盖检查纳入 CI

- 源码路由复核发现 `POST /api/requests/{id}/cancel` 已实现，但 OpenAPI 之前只列了详情 GET 路径。本批补充取消接口的请求方法、管理员认证、200/400/401/409 响应和遗留 running 记录收口说明。
- 新增标准库 Python 脚本 `scripts/check_openapi_routes.py`，从 Server.Handler 解析字面路由，使用明确的动态前缀映射核对模型/请求详情与取消/Provider/Benchmark/API Key 族，以及 Agent 公共协议路径；CI 新增一步运行该检查。
- 对账范围是路径集合，不是完整的 Handler JSON Schema 等价性。后续仍应将每个新增 operation 的方法、状态码和字段契约与 Handler Review 结合；本功能不把 OpenAPI 的 partial 状态改为 complete。
- 候选提交已准备，但 Actions #215（Head `dba3892`）尚未结束；不会在当前完整回归期间更新远程分支，避免 CI 重叠。

## 2026-10-26 追加：并发许可器测试的超时清理

- PR #7 有一条未解决的自动 Review P2（`internal/provider/openai_compatible_test.go`）：首个请求等待启动超时后直接 `t.Fatal`，若上游 mock handler 已进入 `<-releaseFirst`，延迟的 `server.Close()` 会等待被阻塞的 handler，导致测试卡住。
- 修复是在 startup timeout 分支先关闭 `releaseFirst`，再触发 Fatal，确保 handler 有机会结束。此修改只影响测试失败清理，不改变并发限制生产逻辑或成功路径断言。
- 本修复先作为候选提交保存；当前 Actions #216 尚在执行，不在该轮完整回归中途推送下一 Head。新 Head 的回归结果仍以 GitHub Actions 为准。

## 2026-10-26 配置治理第一批：模板与环境变量契约

- 本批已补齐 logging.retention_days、retention_batch_size、retention_interval_seconds 和 agents，并使本地请求时限引用 LLAMA_REQUEST_TIMEOUT_SECONDS。紧随其后的提交将 database.host 与 redis.addr 移为环境变量引用，并通过 Compose 默认值兼容旧 .env。
- 本批对齐配置路径和超时来源，仅保留数据库、Redis 服务名作为已登记部署差异；新建静态契约检查，校验 Go YAML 字段、两个配置文件的字段路径/默认值、YAML 环境变量与 Compose 白名单，以及 Provider 环境变量支持字段与示例。
- 新 CI 检查只验证静态配置结构与注入链路，不代表实际 YAML 启动、真实凭据、远程 Provider 连通性或数据库运行时已验收。按用户约定不手动跑测试；以本批提交后的 GitHub Actions 为准。
- 继续事项：更完整地核对 Web/数据库 Provider 的热更新、跨来源冲突和默认值兼容，并在统一回归前不改变当前 config.yaml 跟踪策略。


## 2026-10-26 配置治理第二批：数据库/Redis 服务地址归入环境层

- 配置模板不再把 MariaDB/Redis 的网络地址硬编码在 YAML：统一引用 `LLM_GATEWAY_DB_HOST` 与 `LLM_GATEWAY_REDIS_ADDR`，主 `.env.example` 提供本部署的示例值。
- Compose 对未更新的旧 `.env` 回退到 `Mariadb` 与 `Redis:6379`，不要求升级时马上手动添加新变量。两份 YAML 静态字段和值完全一致，CI 不再为这两项保留例外。
- 本批提交对应 Actions #40302076802（#223），完整 34/34 步已通过。


## 2026-10-26 OpenAPI operation 元数据门禁

- 为现有 72 个 OpenAPI operation 增加唯一 operationId，并将 CI 检查扩展到 summary、responses 和显式 security。仅 `GET /health` 与 `POST /api/auth/login` 为公开 operation；完整 Actions #224（运行 `40302664234`）已通过。
- 运行时路由路径覆盖为 67/67（其中包括 Agent 前缀展开）；OpenAPI 本身当前有 63 个字面路径模板和 72 个 operation。尚未自动验证每个请求/响应 Schema 与 Go Handler 序列化的等价性，因此保留 OpenAPI 的 partial 状态。

## 2026-10-26 请求日志保留运行状态 API

新增 `GET /api/admin/retention/status`。在管理员鉴权后返回保留配置与进程内聚合指标，包括最近执行状态、执行次数、成功/失败次数、执行时间/耗时、删除行数、累计删除量和稳定失败代码。完整 Actions #225（34/34 步）已通过。

该接口只读取内存状态，设置 `Cache-Control: no-store`；原始数据库异常只进入 Gateway 服务日志。计数在进程重启后清零；外部监控器可轮询 `last_run_status`/`failure_count`，Gateway 本身不推送告警。此功能和 OpenAPI 契约已合入 `dev` 并由完整 Actions #225 验证通过。


## 2026-10-26 Provider 每日 Token 预算实现增量

- 为远程 Provider 增加 `token_budget_per_day`，0 默认关闭；YAML、`LLM_GATEWAY_PROVIDER_<ID>_TOKEN_BUDGET_PER_DAY` 与 Web CRUD 统一支持该字段，管理台显示当前 UTC 日桶的已用/预占、余额和重置倒计时。
- Redis Lua 实现每日预算原子预占和结算；多个 API Key 与 Agent 按 Provider ID 共享额度。准入估算为请求体 UTF-8 字节数加单次输出上限，属于保护性估算，不是供应商 tokenizer。只有可信供应商 usage 会用于最终结算。
- 无可信 usage 的已出网上游请求保留预占到 UTC 日桶过期，避免在供应商可能已计费时错误退款。实际用量超过估算时会按上游真实数记账，已开始的请求无法被事后撤回。
- 本批新增 Go 配置/环境变量、Redis 预算集成用例、输出 Token 限制用例、Web UI、Compose 白名单、OpenAPI 和中文文档；不手动运行测试，以本批提交后的 GitHub Actions 作为自动化验证。


## 2026-10-27 Provider 有界 FIFO 等待队列

新增 `max_queue_size`（0 关闭、最大 1024）和 `queue_wait_timeout_seconds`（0 使用默认 30 秒、最大 3600 秒）。队列关闭保持旧版立即拒绝；开启时按 FIFO 等待、队列满或超时返回 503、客户端取消立即移除排队项。管理台增加排队深度/累计超时指标。该状态仅限单个 Gateway 进程；多副本全局并发与排队协调仍未实现。本次补齐单测并等待 GitHub Actions 对新提交的自动验证，生产慢上游与代理链路尚未实测。
