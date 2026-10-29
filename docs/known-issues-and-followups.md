# LLM Gateway 未关闭问题与后续验收台账

> 更新日期：2026-10-29。此文件是问题统一入口。关闭事项时附 commit、CI 运行与可复查结果；真实供应商和生产部署验收单独标记，不以 Mock/CI 代替。

## 当前基线

- 当前最新 `dev` HEAD 为 `52d58f6c7c549902d69692a6f2741724b943acf2`；该提交补充 Provider 新增重复 ID 时的 OpenAPI `409` / `provider_id_exists` 契约门禁。最新 Go CI [运行 40821989516](https://github.com/Kasign/llm-gateway/actions/runs/40821989516) 已全部 5 个 Job 成功。
- 配置/API 文档统一与防回归门禁提交 `3cf0636316582fff1b3a57ae8bfa5074a23c6a05` 的 Go CI [运行 40821447994](https://github.com/Kasign/llm-gateway/actions/runs/40821447994) 已全部 5 个 Job 成功，覆盖 Go 测试/race/vet、配置/API 契约、前端 Bundle、稳态负载与审计持久化、故障注入和 Docker 构建。
- 最近一次 Web UI 浏览器验收 [运行 40816685743](https://github.com/Kasign/llm-gateway/actions/runs/40816685743) 在 `8ece8c65d6884cbe0d104a343dcb5fce501c9e94` 上通过；之后的提交只修改配置/契约文档与检查脚本，没有改变前端运行时源码。
- 目前没有开放 PR 或 Issue（2026-10-29 复核）。真实供应商、公网 OpenResty/公网链路和 1Panel 部署验收仍未完成；Mock/CI 不作为实际生产验收证据。
- 先前显式设置 `llama.request_timeout_seconds: 0` 被代理层强制套用一小时的问题，已有代码级修复和零值/正值回归覆盖；当前完整 Go CI 已通过。慢模型在实际 OpenResty、客户端和本地模型组合下的端到端超时行为仍需生产环境验收。
- Provider 热更新已有共享并发许可器、限额变更顺序和回滚测试；后续只针对新的可复现失败补充实现，不再重复扩充同一段抽象。
- 历史记录中在旧提交上标记为“待运行”的测试，需以本基线之后对应工作流的最终结果为准；不把旧失败继续算作当前阻塞，也不因为 CI 成功而关闭真实环境验收项。

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
| GW-GOV-002 | Provider 级 RPM、每日 Token 预算与可选排队已接入。 | Provider RPM、每日 Token 预算与有界 FIFO 队列已由后续完整 CI 覆盖；真实供应商用量与公网链路仍待部署环境验收。 | `requests_per_minute=0` 默认关闭，正数按 UTC 固定分钟窗口计数。`token_budget_per_day=0` 默认关闭；启用后对同一 Provider 跨 API Key/Agent 共享 Redis UTC 日桶，原子预占并在可信供应商 usage 到达后结算。排队默认关闭（`max_queue_size=0`），启用后进程内 FIFO、有界 1024，支持 Context 取消及 503 超时/满队列错误；不将它与 Redis RPM 或 Token 预算混为一套状态。 | CI 对逻辑与构建验收通过；真实用量差异、慢模型及反向代理端到端行为保留在统一部署回归。 |
| GW-GOV-003 | 自动重试/故障转移尚未实现。 | 明确暂缓，非当前缺陷 | 定义幂等性、已提交响应判定、Provider Retry-After、预算与重复请求语义；SSE 开始输出后不得换 Provider 产生第二份答案。 | 经架构 Review 后独立决策；未批准前不启用自动切换。 |

## P2：外部兼容与产品增强

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-E2E-001 | OpenAI-compatible Chat、Responses 与 Anthropic Messages 的 opt-in 真实端点用例存在，但 CI 默认跳过，没有实际调用真实供应商。 | 待授权环境验收 | 在用户控制的安全环境提供 HTTPS BaseURL、Provider 专用 Key 和模型名；确认计费额度后执行短请求；禁止将 Key 写入仓库、日志与 Artifact。 | 记录供应商、模型、端点、日期、状态/协议核验结果与脱敏日志；开发 CI 默认不强制联网。 |
| GW-E2E-002 | 真实供应商各模型的工具调用、结构化输出、Reasoning、视觉、错误封套、真实限流与余额不足行为并未全部验证。 | 部分依赖供应商 | 按 Provider × API × 能力建立兼容矩阵，只验证有实际需求的组合。 | 每个矩阵单元注明通过、失败、不支持或未测；一家通过不代表所有厂商通过。 |
| GW-PROV-UI-001 | Web 多 Provider 管理与配置来源统一。 | 远程 Provider 已统一由 Web/MariaDB 管理；前端阻止已知重复 ID，后端再次归一化校验并返回 409，不覆盖旧配置；旧状态字段已从列表和 API 契约移除。当前 HEAD 的 Go CI 全部通过。 | 后续仅在复现新问题时修复运行时逻辑；旧 YAML/环境变量预置路线不再恢复。 | 用户部署前备份数据库与 `LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY`；真实供应商、公网反代与 1Panel 行为仍需环境验收。 |
| GW-AGENT-001 | 同一 Agent 内多模型别名映射、Agent 级不同 Key/权限策略和配置热更新尚未实现。 | 待产品需求确认 | 保持 Agent 固定 Provider/默认模型的现有契约；需求明确之前不擅自增加权限与路由复杂性。 | 配置模型、优先级、热更新一致性和多 Agent 隔离均有测试。 |
| GW-API-001 | OpenAPI 的字段级 Schema 与 Go Handler 语义等价性仍需逐步补齐。 | 新增高风险字段/状态契约门禁，覆盖 Provider mutation/view 预算字段、创建/删除响应及统计数据库错误封套；整体 OpenAPI 仍为 partial。 | 继续在实际功能改动时对照 Handler 的请求体、成功状态码、错误响应和稳定 JSON 字段；不一次性声称全部契约完成。 | 已覆盖高风险子集；一般性请求/响应 Schema 的全量 Go 等价性仍需分阶段补齐。 |

## 当前执行范围

1. Provider 热更新已完成最后一轮静态审查：没有确认正常配置路径可触发的 setter 失败，不增加额外回滚框架或重复测试。
2. 修复显式零整体超时被代理层提升为一小时的问题，并为实际代理 Context 增加零值/正值回归测试。配置未指定时仍由配置加载逻辑应用既有的一小时默认值。
3. 密钥脱敏、Provider 超时、取消与 SSE 提交边界已进行静态 Review；前端源码与部署 Bundle 由 Actions 中的构建后差异检查验证。
4. 代码级验收依新 Head 的完整 GitHub Actions。真实供应商 E2E、公网 OpenResty 与 1Panel 部署验收依赖用户环境，单独记录，不与 Mock CI 混为一谈。
5. 继续暂缓自动重试/故障转移、Agent 权限体系、全量 OpenAPI 对账及目录迁移；只有确认缺陷或明确产品需求才重新纳入范围。

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


## 2026-10-27 高风险管理 API 契约门禁

- 已核实 `GET /api/requests/stats` 对数据库错误保留服务端日志、向客户端返回稳定的 `500 database_error`；`TestWriteRequestStatsDatabaseErrorHidesInternalDetails` 验证不泄露数据库诊断。
- Provider 创建 Handler 返回 `201 Created`，删除返回无正文的 `204 No Content`；OpenAPI 对应成功响应保持一致。
- 新增标准库脚本 `scripts/check_openapi_contracts.py`，检查 Provider mutation/view 的并发、队列、RPM、每日 Token 和超时字段/范围，以及创建、删除、统计失败响应的关键契约，并加入 Actions 前置门禁。
- 这是字段级覆盖的起点，不代表整个 OpenAPI 与 Go Handler 已完全等价；`x-contract-status: partial` 继续保留。Provider 队列功能对应 Actions [40434515661](https://github.com/Kasign/llm-gateway/actions/runs/40434515661) 已全量通过。


## 2026-10-27 FIFO 等待超时边界复审

- 复审发现：如果等待定时器已触发，但 goroutine 因调度/锁竞争尚未恢复，而并发许可在截止时刻后被发放，原逻辑可能把晚到许可当成成功。
- 现在在等待项入队时确定 deadline，并记录每个许可真实发放时间；ready 与 timeout 同时就绪时，严格以发放时间判定。超时后到达的许可会立即归还、唤醒后续等待者并记录一次队列超时。
- 新增确定性测试：测试持有许可器互斥锁跨过队列截止时刻，再发放许可，验证 503 `provider_queue_timeout`、活动许可归零及队列/超时指标正确。
- 本修复为小范围时序修正；本次不手动运行测试，等待新 Head Actions 完整回归。

## 2026-10-27 管理台本地 GGUF 列表故障隔离

- Review 发现本地模型页把 `GET /api/models`（数据库中的已登记 GGUF 清单）与 `GET /api/admin/runtime`（当前上游运行状态）用 `Promise.all` 强绑定；运行时接口失败会让整个刷新 Promise 拒绝，导致已成功读取的本地清单也无法渲染。
- 已将两个请求改为独立收敛结果：运行时接口不可用时仍渲染本地模型表，并提示运行时状态暂不可用；清单接口失败时展示独立错误，不再把接口失败误报成“暂无模型”。刷新失败时保留最近一次成功读取的列表，并明确标识它是旧快照。
- 运行概览会把运行时不可用与“没有模型加载”区分开。错误提示使用稳定通用文本，不将上游原始错误或内部地址直接展示到页面。
- 本次已同步管理台源码和部署 bundle。以该修复提交对应的 GitHub Actions 校验 bundle 一致性、语法及 Go 构建；未声称已连接真实 llama.cpp / Ollama 实例验证。

## 2026-10-27 部署托管 Provider 的 FIFO 配置透传

- 启动注册链路复审发现，`newInferencePipeline` 构建 YAML/环境变量 Provider 时未把 `max_queue_size` 与 `queue_wait_timeout_seconds` 传给 OpenAI-compatible Provider 构造器；Web/数据库 Provider 的构造路径此前已透传这两个字段。
- 影响范围仅为部署托管 Provider：配置值虽然通过启动校验，运行时却采用队列关闭和默认等待时限，导致配置与实际准入行为不一致。
- 已修复构造器参数透传，并新增 Server 层回归用例：检查启动注册后的队列容量，并在许可槽被占用时验证配置的 1 秒等待期限会返回 `503 provider_queue_timeout`。
- 以提交后的 Actions 验收该链路；此测试验证本地构造与队列行为，不代表真实供应商可用性或生产容量验收。

## 2026-10-27 预算预占与 Provider 队列准入边界修正

- Review 发现 Pipeline 的 `UpstreamStart` 时间戳在调用 Provider 前写入；但 Provider 之后仍可能因并发槽位占满、FIFO 队列已满或等待超时而拒绝请求，未实际调用上游 HTTP。
- 旧预算结算逻辑将 Pipeline 时间戳误当成已出网证据，导致这类请求没有可信 usage 时把预占 Token 保留到 UTC 日桶过期，可能提前耗尽日预算。
- 已改为由 OpenAI-compatible Provider 在通过准入、即将执行 HTTP 请求时回调标记预算预占。未发起 HTTP dispatch 的请求退还全部预占；已发起请求若缺少可信供应商 usage，继续保守保留预占，避免对可能已计费请求错误退款。
- 新增 Provider dispatch 回调准入/拒绝用例，以及依赖 `REDIS_TEST_ADDR` 的服务端预算回归：验证未 dispatch 时从 Redis 预算中全额退回，dispatch 后无可信 usage 时仍保留。无 Redis 的本地运行环境会跳过这条集成用例；真实 Provider 账单仍需按各自范围验收。

## 2026-10-27 本地模型清单 API 字段契约收口

- 本地模型列表故障修复后，对照 Handler 发现 `GET /api/models` 实际返回完整 `GatewayModel` 行，但 OpenAPI 的 `data.items` 仍声明为任意 object，没有引用仓库中已经存在的详细 Schema。
- 已将响应改为引用 `GatewayModel`，并扩展高风险 OpenAPI 检查脚本：检查该响应引用、Go `model.Model` 的所有 JSON tag，以及 `ListGatewayModelRows` 合成的上游名称/ID与磁盘大小字段均存在于 Schema。
- 这只关闭本地模型列表端点的一项字段漂移缺口；OpenAPI 对所有 Handler 的全量字段级等价检查仍是分阶段工作。

## 2026-10-27 CI 提速：并行独立门禁与缓存镜像层

- 旧工作流将配置/前端检查、Go 测试、负载故障注入、竞态、性能基线、静态检查和 Docker 镜像构建放在同一个 Job 串行执行；任何阶段的耗时都会阻塞后续互不依赖的检查。
- 已拆为四个并行 Job：配置/API 契约与前端、Go 测试/竞态/静态检查、Gateway 负载与故障注入、Go/Docker 构建。保留原有覆盖范围及请求负载、故障注入产物。
- 删除独立重复的 MariaDB 审计用例步骤（该用例仍在完整 `go test ./...` 中执行，并在负载 Job 保留专项运行），避免基础测试重复消耗 Runner。
- Docker 镜像使用 Buildx GitHub Actions 层缓存，Node/npm 启用依赖缓存；同一分支新提交会取消过期 CI，防止排队中的旧 HEAD 持续占用 Runner。
- 此变更的实际提速幅度以新工作流运行时间为准。并行 Job 会增加同时启动的 Runner/数据库服务，不减少关键验证覆盖，也不声称能加速用户本地模型的真实推理耗时。

## 2026-10-27 请求日志筛选与取消 API 契约补强

- 复核发现 `POST /api/requests/{id}/cancel` Handler 会对非 POST 返回 `405 method_not_allowed`，但 OpenAPI 未声明 405，客户端契约与真实 Handler 不完整。
- 已补齐 OpenAPI 405 响应，并扩展高风险契约检查器：核对取消端点的 200/400/401/405/409 状态集合、405 错误封套，以及 Handler 实际的 POST/状态码分支。
- 同时核对 `GET /api/requests` 的 `provider` 字符串查询参数确实在 Handler 中传入合并日志查询，而非只有文档声明。该门禁仅覆盖此请求日志 API 子集，完整 Go/OpenAPI 字段对账仍分阶段推进。

## 2026-10-27 Provider 热更新队列预算回归覆盖

- 新增 `TestPrepareProviderQueueLimitAppliesChangesAtSafeTime`，覆盖队列容量缩小时前置应用并可回滚、扩大延后到 Registry 更新后、已停用 Provider 编辑不修改运行时许可器、重新启用时延后放宽，以及越界容量拒绝且不更改当前值。
- 这是热更新事务的回归覆盖，不改变运行时队列策略；FIFO 状态仍局限于单个 Gateway 进程，不代表多副本全局排队。

## 2026-10-27 CI 稳态负载与故障注入并行化

- Actions #241/#242/#243 已确认四个主 Job 并行后总时长约 2 分钟；其中 Gateway Job 仍把 60 秒稳态负载、故障注入和连接/DNS/SSE 恢复测试串行执行。
- 已将稳态负载与故障注入拆成两个独立并行 Job。MariaDB 审计积压用例和 12,000 次/目标 200 req/s 稳态负载保留在前者；429/503、TCP 拒绝与恢复、DNS、已提交 SSE 后断流、Provider TLS 握手测试与原有 Artifact 保留在后者。
- 该拆分额外启动一套独立 Redis/MariaDB 服务，预期以少量 Runner/容器成本缩短墙钟时间；提速幅度以新 Head Actions 实测为准，不通过删减真实负载/故障覆盖提速。

## 2026-10-27 请求日志 DTO 的 OpenAPI 字段对账

- `GET /api/requests` 的 `RequestList.data.items` 和 `GET /api/requests/{id}` 的 `log` 原先均为泛化 object，未引用真实持久化 DTO。
- 新增 `RequestLog` Schema，并扩展高风险契约检查器，对照 `internal/store/store.go` 中 `store.RequestLog` 的全部 JSON tag；列表与详情共用一个 Schema，减少字段漂移。
- 这仅覆盖稳定审计 DTO；详情的 `summary`、`timing`、`analysis` 和 SSE 事件仍有动态/条件字段，整体 OpenAPI 状态继续保持 partial。

## 2026-10-27 请求详情摘要与耗时字段契约

- 请求详情中的 `summary` 与 `timing` 原先仅声明为任意 object；现在分别引用 `RequestSummary` 和 `RequestTiming`，明确稳定的状态、模型、请求大小、耗时、TTFT 与 Token 来源字段。
- 高风险 OpenAPI 门禁从 `buildRequestSummary`、`buildRequestTiming`、`buildRequestDetail` 和 `applyHarvestToTiming` 中提取 map 键，并检查每个稳定字段都有 Schema 定义；新增字段需要同步更新契约，避免管理台显示字段无文档。
- 动态正文、请求/上游 Header、分析内容和流式事件仍按配置与请求结果有条件出现，本次不将它们误标为必填。

## 2026-10-27 请求详情分析与流事件 DTO 契约

- 将 `RequestDetail.analysis` 与 `stream_events.items` 从泛化 object 改为 `RequestAnalysis` / `StreamEvent` Schema，明确阶段耗时、吞吐、Token 来源和流事件字段。
- 高风险契约门禁现对照 `request_analysis.go` 中 `RequestAnalysis`、`RequestPhaseMS`、`RequestThroughput` 与 `store.StreamEvent` 的 JSON tags；详情正文、Headers 与分析备注仍按保存策略动态出现。

## 2026-10-27 RequestAnalysis 门禁脚本语法修复

- Actions #247 在高风险 Schema 检查阶段发现新增 DTO 字段对账脚本两处集合推导式条件行有非法尾随逗号，Python 解析阶段报 SyntaxError；不是生产 Go 代码或 OpenAPI Schema 不匹配。
- 已移除这两处逗号，保留 RequestAnalysis/RequestPhaseMS/RequestThroughput 与 StreamEvent 全字段核对。新 Head 需重新执行完整 CI；不以本次失败运行作为验收结果。

## 2026-10-27 RequestAnalysis 门禁第二轮修复

- Actions #248 的契约脚本在语法修正后暴露第二个顺序问题：新增 RequestAnalysis/StreamEvent 检查使用了 `request_detail_schema`，但赋值位于后面的 RequestLog 检查段，触发 `UnboundLocalError`。
- 已将 RequestDetail Schema 初始化移动到首次使用之前，保留 RequestAnalysis、RequestPhaseMS、RequestThroughput、StreamEvent 与其他字段的所有检查；没有绕过高风险门禁。以新 Head Actions 验证完整结果。

## 2026-10-27 请求摘要动态字段与分析子 Schema 检查补强

- 进一步 Review 发现摘要 helper 的 `s["field"] = value` 写法未被原有契约提取正则覆盖；现已加入赋值键检查，并明确要求 `RequestAnalysis.phases_ms` / `throughput` 分别引用 `RequestPhaseMS` / `RequestThroughput`。
- 该改进避免 Schema 看似已定义但动态赋值字段或嵌套引用静默漂移；本批只加强静态门禁，不改响应的运行时数据。

## 2026-10-27 本轮开发基线与提速记录

- 最新已核实集成基线为 `4122fb9a002c83603c6e1552f6324c4f82194311`；该提交对应 Actions #249 全部通过。前两轮 RequestAnalysis 契约门禁失败是脚本语法与变量初始化顺序问题，已修复并由后续完整 CI 验证通过。
- CI 已按配置/API 契约、Go 测试与静态检查、稳态负载、故障注入、构建拆分并行执行，并启用 npm/Buildx 缓存和同一 ref 过期运行取消；此前完整运行从约 4 分 10 秒降至约 2 分钟。
- 后续提速原则：将相关生产代码、回归覆盖、OpenAPI 与中文文档合并为合理的功能批次，避免逐文件提交触发重复 CI；继续保留 race、负载和故障注入，不通过删除验证步骤换取表面速度。

## 2026-10-27 RequestDetail 顶层返回字段收口

- `RequestDetail` 现显式声明正文/预览、字节数、存储提示、Token 重解析提示、请求参数、请求/上游 Headers、聚合 content/reasoning、流事件数量/说明与实时标记，不再使用宽泛的 `additionalProperties: true`。
- 高风险契约检查器会从 `buildRequestDetail` 相关 helper 及 `requestDetailAPI` 中提取 `out["field"]` 顶层写入键，并要求每个键都在 Schema 中，防止有字段实际返回但 OpenAPI 不知情。
- 正文和 Headers 字段仍是可选项，具体是否存在由 `logging.store_payload`、历史记录与请求状态决定。

## 2026-10-27 运维状态 API 字段契约

- 将 `GET /api/admin/audit/metrics` 的 `data` 从泛化 object 改为 `AuditMetrics`，完整声明队列容量、写入/丢弃计数、丢弃率和排队等待耗时；CI 对照 `audit.StatsSnapshot` 的 JSON tags。
- 将 `GET /api/admin/retention/status` 的 `data` 改为 `RequestRetentionStatus`，并以 `RequestRetentionRuntimeStatus` 描述最近运行状态、成功/失败计数、删除量和稳定失败代码；CI 对照两种 Go 响应结构的 JSON tags。
- 两个接口均为只读内存快照，原始数据库错误或请求正文不会从这些状态对象泄露。


## 2026-10-27 限流器故障响应脱敏

- `serveInference` 原先在 Redis 限流器失败时，将 `err.Error()` 同时传给客户端响应和请求审计，可能泄露内部连接或依赖诊断。
- 已改为固定错误文案 `rate limiter unavailable`；HTTP 503 与错误码 `rate_limit_unavailable` 保持不变，详细错误仅保留在服务端日志。
- 同轮还收紧了模型解析失败与本地模型加载失败的调用方消息，避免将上游控制 API 的原始错误透传；原有稳定错误码和 HTTP 状态不变。
- 当前 Head 的自动化验收待完成；此修复不替代真实部署中 Redis 故障处理验收。

## 2026-10-27 热更新与整体 Review 收尾

- `dev` 在本批修改前为 `62780c7bab6c3245ec4783a9b0ccbcc7a61d865f`，Actions [#256](https://github.com/Kasign/llm-gateway/actions/runs/40453353753) 全部通过；当前代码变更仍以新 Head 的 Actions 结果验收。
- Provider 热更新：正常输入在保存前校验并发、队列范围；运行时构造还会校验并发范围；降低额度的前置应用有回滚路径，提高额度在 Registry 更新后才应用。没有发现可由有效配置触发的 setter 错误，不为理论异常扩大事务实现。
- 配置兼容 Review 发现 `proxyRequestContext` 将显式 `request_timeout_seconds: 0` 误当作缺省值并强加一小时 Deadline，与配置加载、示例和文档的语义冲突。本批改为复用可选超时 helper：显式 0 不增加整体 Deadline，父 Context 的取消仍传播；未设置该项时仍由 `Config.Load` 维持旧的一小时默认值。
- 本批只增加代理 Context 的零值/正值回归覆盖；未重复运行手工测试。CI 构建检查保留前端源码和 `web/static/dist` Bundle 一致性门禁。
- 真实 Provider、慢模型经公网反向代理的实际超时/断连表现，以及 1Panel 部署仍未由这次代码审查或 CI 验收；继续作为用户环境验收项。

## 2026-10-27：llama.cpp 模型列表兼容性修复

- 根因：`llamacppRuntime` 和 `Client.List` 仅在 `GET /models` 请求失败时回退到 `GET /v1/models`；若 Router 列表返回 HTTP 200 但 `data` 为空/无有效 ID，单模型模式的已加载模型不会被查询，界面可能把空目录当成完整结果。
- 处理：优先使用包含有效模型 ID 的 Router 全量目录；目录为空或无有效 ID 时再查 OpenAI 兼容接口。`/v1/models` 返回的模型按其协议语义标记为已加载，避免缺失 Router `status` 字段时显示为未加载。模型候选 ID 的 `Client.ListModelIDs` 复用相同的回退逻辑。
- 新增 `internal/llama/runtime_models_test.go`：覆盖 Router 空目录时从 `/v1/models` 发现模型、加载状态正确，以及 Router 有目录时不被单模型响应覆盖。未手动运行测试，按用户安排交由后续统一验证。
- CI 阻塞仍单独记录：Actions #257 的所有 Job 均在实际步骤前失败，当前没有足够证据将其归类为代码缺陷，也未确认是否为 GitHub Hosted Runner 配额/权限问题。


## 2026-10-27：llama.cpp 全模型目录与实时加载状态

- GGUF 管理表统一合并 Router 注册目录、Gateway 共享磁盘目录和 Gateway 数据库登记项。磁盘扫描只读取文件元数据，递归扫描子目录、忽略 `mmproj*.gguf`，多分片模型合并为一条逻辑记录。
- `active_model` 只表示上游实时报告已加载的模型；`gateway_model` 独立表示 Gateway 当前默认路由目标。数据库历史 `status=loaded` 不再作为实时证据。
- 新增管理员 POST `/api/admin/runtime/refresh` 作为显式目录刷新入口；刷新会请求 Router 重扫并清空快照缓存。空 `/models` 列表仍可识别为 Router。
- 非 Router 单模型服务不再将跳过 `POST /models/load` 的目标伪装成加载成功；只有 `/v1/models` 确认目标已加载时才接受请求，否则返回 Router 模式限制。
- 新增磁盘分片扫描与空 Router 检测回归用例。本批未手动运行测试或构建；Actions #257/#258 均在作业步骤开始前失败，代码仍待完整 CI 验收。

## 2026-10-27 追加：Docker 构建的未使用导入错误

- 用户的镜像构建输出指出 `internal/llama/runtime.go:7` 导入 `fmt` 但未使用；检查文件确认没有 `fmt.` 调用，已移除该导入。
- 该修复只解决 Go 编译错误，不改变运行逻辑。未手动重跑构建或测试；最新 GitHub Actions 曾在 Runner 分配前失败，不能据此确认新提交构建通过。
- Compose 同时出现两个未设置变量的警告。它们与 Go 编译错误分开处理；变量名可能源自本地 `.env` 中包含美元符号的值，需检查相关密钥是否被 Compose 当作插值表达式解析，不要将真实密钥提交到仓库或复制进日志。

## 2026-10-27 追加：密码/密钥输入明文切换

- 管理台登录、账号凭据、管理员 API Key、Provider API Key 等密码输入默认隐藏，并增加眼睛图标用于显示/隐藏。
- UI 切换不改变提交与密钥存储行为；使用独立按钮和无障碍标签。
- 修改 `web/index.html`、`web/static/css/app.css` 及同步实施计划。此前逻辑放在 HTML 内联脚本中，在存在 CSP 限制的反向代理环境下可能被浏览器拦截；已将按钮初始化迁移到 `web/js/features/auth.js` 并同步更新 `web/static/dist/app.js`，登录提交也改为由 Bundle 绑定，移除 HTML 内联脚本和 `onsubmit`。
- 本次未运行浏览器测试或构建，待用户统一验收。


## 2026-10-27 追加：登录哈希与 Compose 插值解耦（旧实现，已被后续方案替代）

- 将 Web 登录哈希从 Compose `environment` 的 `${LLM_GATEWAY_ADMIN_PASSWORD_HASH}` 直接映射移除，改由独立 `admin-auth.env` 以字面字符串注入。主 `.env` 不再保存同名变量，从根源减少 PBKDF2 哈希美元符号触发未定义变量警告的机会。
- 新增 `admin-auth.env.example`，并将真实凭据文件加入 Git 忽略和 Docker 构建上下文忽略规则。
- 数据库无 `web_admin_login` 行时，应用使用配置账号与哈希；不应通过删表或清空其他设置来处理登录失败。
- 部署端结果尚待验证：检查 Compose 渲染警告、容器内哈希分段数及 `/api/auth/login` 的 HTTP 状态后再判定登录故障是否关闭。

## 2026-10-27 追加：密码单凭据与可选保护模式

- 控制台登录改为仅输入密码；新增 GET /api/auth/status 用于读取密码保护开关。关闭保护后前端会自动建立 Web 会话，但管理 API 仍复用同一会话鉴权层。
- 设置页新增保护开关。启用状态下改密/关闭保护需要当前密码；未启用状态打开保护需要输入新密码。界面用普通用户易懂的提示说明关闭保护后的影响。
- 开关与哈希使用现有 web_admin_login JSON 设置记录持久化，兼容旧记录：缺少开关字段时，有效哈希视作已启用。空库不需要删表；没有该记录时使用部署配置。
- 本批完成后仍需部署回归验证：状态接口、启用密码时的 200/401 行为、无密码模式会话建立及设置保存。

## 2026-10-27 追加：密码保护重构代码 Review 修复

- 登录处理改为从同一份网页登录快照读取密码保护开关、密码哈希和会话版本，避免并发修改时读取到不一致状态；密码设置写入增加串行锁，避免并发请求覆盖更新后的版本。
- 账号设置加载后同步登录页的保护模式，修复用户在当前页面开启/关闭保护后，登出再进入时登录页仍显示旧模式的问题。
- 移除 `.env.example` 中已废弃的 `LLM_GATEWAY_ADMIN_USERNAME`，同步修正配置矩阵、API/数据库契约及密码恢复说明；明确数据库已有 `web_admin_login` 时不再由部署哈希覆盖，也不应删表或清空数据库。
- 对齐密码明文显示/隐藏图标源码与静态 Bundle。
- 本轮只进行代码与文档静态 Review，未运行构建、测试或 CI；待用户统一验收。

## 2026-10-27 追加：登录切页与密码明文切换 Review

- 修正 `.shell[hidden]` 的样式优先级，避免 `.shell { display:grid }` 覆盖隐藏状态，让未登录控制台从布局和键盘焦点顺序中真正移除。
- 登录成功与账号设置保存后，清空密码输入框时同步恢复 `type=password`、眼睛图标、`aria-label` 和 `aria-pressed`，避免下一次输入时保留明文显示状态。
- 登录会话签发与密码设置更新共用状态锁，避免密码/开关更新期间签发旧版本会话。
- 修正配置说明中残留的旧用户名条目和删除 `web_admin_login` 的 SQL 指引，明确重启与重建数据库的差别。
- 未运行构建、测试或 CI；静态 Bundle 与源码已同步修改，仍需用户统一验收。


## 2026-10-27 追加：哈希表示与登录眼睛按钮修正

- 前一轮独立 `admin-auth.env` 的方案在用户部署环境仍出现哈希片段被 Compose 解释为变量的警告，现改为主 `.env` 配置新格式 `pbkdf2:`，由 Compose 显式映射进容器；不改变现有派生算法，并继续支持历史 `pbkdf2$` 哈希。
- 增加 `LLM_GATEWAY_WEB_PASSWORD_ENABLED`，只作为数据库尚无账号设置时的初始默认值；数据库已保存的状态继续优先。
- 登录页的眼睛按钮改为在 `web/index.html` 中静态存在，JavaScript 负责绑定；同步修改源码和静态 Bundle。
- 本次按用户要求只做静态代码与文档修改，未运行构建、测试或 CI；部署结果仍需由用户统一验收。

## 2026-10-27 追加：诊断并恢复数据库优先导致的控制台密码锁定

- 用当前 Go 密码校验实现独立运行了回归测试：部署模板中的冒号分隔哈希匹配 `999888777`，旧哈希仍匹配旧占位密码。因此在部署哈希正确但新旧密码均失败时，优先检查数据库是否已有 `gateway_settings.web_admin_login`。
- 新增临时 `LLM_GATEWAY_WEB_LOGIN_RESET_FROM_ENV=true`，启用时跳过数据库中的旧登录凭据并使用部署哈希。账号设置保存新密码后，必须撤销该开关并重建容器。
- `/api/auth/status` 返回不含秘密的 `password_source`，启动日志同时记录来源、密码保护状态与哈希格式，不记录哈希内容。
- 新增恢复环境变量解析与默认哈希的回归测试；已运行独立 Go 密码包测试，尚未运行完整 Go 测试或 Docker 构建。

## 2026-10-28 最新基线与收尾任务

- 当前 `dev` HEAD：`654416290a65090648080ffce3214bddb89b916f`。Go CI [40631304311](https://github.com/Kasign/llm-gateway/actions/runs/40631304311) 与浏览器验收 [40631304282](https://github.com/Kasign/llm-gateway/actions/runs/40631304282) 均成功；浏览器验收报告为 52/52 项通过，未发现页面 JS 异常、控制台错误或静态资源加载失败。
- 2026-10-28 表单视觉修复：普通输入框与下拉框统一为 42px 高度；调用记录与模型下载页增加控件高度一致性断言；Provider 状态徽标设置为不换行，并加入样式探针验证。CSS 缓存版本同步更新。对应提交：[6544162](https://github.com/Kasign/llm-gateway/commit/654416290a65090648080ffce3214bddb89b916f)。
- 浏览器验收使用 GitHub Actions 隔离环境。Provider 列表在 CI 中不保证有真实配置，状态徽标断言验证公共样式规则与探针，不视为真实 Provider UI/网络端到端验收。
- 当前没有开放 PR 或 Issue。代码级工作暂不继续扩大；待办优先级为：P1 生产 1Panel/OpenResty/llama.cpp 实际部署验收；P1 真实 Provider opt-in E2E；P2 管理台轮询性能基准；P2 按风险分批补齐 OpenAPI 字段级契约。
- 生产验收需要实际部署权限、有效供应商凭据和慢模型环境；本次 CI 不会自动证明这些项目通过。自动重试/Provider 故障转移、Agent 级权限/多别名、全量目录迁移继续暂缓。


## 2026-10-28 验收工具材料

- 新增 `docs/production-acceptance-checklist.md`：1Panel/OpenResty/llama.cpp 生产验收步骤与结果模板。未实际运行的项目默认标记“未测”。
- 新增 `docs/admin-polling-performance-plan.md`：无轮询/默认轮询/高频轮询对照设计，区分 Go 微基准与管理台端到端测量。
- 真实 Provider E2E 已有 opt-in 测试：OpenAI-compatible Chat、Responses 与 Anthropic Messages；默认不访问网络。运行前需确认端点、模型、预算与用户授权，不能将默认跳过视为通过。


## 2026-10-28 追加：网页登录 OpenAPI 契约闭环

- 修正 `PUT /api/auth/credentials` 成功响应的 Schema：Handler 除 `user.username` 外还返回 `password_enabled`，不应只引用 `SessionUser`。新增 `CredentialsMutationResponse` 使文档准确描述实际 JSON。
- 将公开 `GET /api/auth/status` 的 `password_enabled`、`password_source` 及 `Cache-Control: no-store` 纳入高风险 Schema/Handler 门禁，防止密码来源状态与公开契约漂移。
- 当前基线 `d89b94247e8b60164f28758a5da1142c5bafa054` 的 Go CI [40639385262](https://github.com/Kasign/llm-gateway/actions/runs/40639385262) 全部 5 个 Job 通过，包含该门禁、前端 Bundle、race、稳态负载、故障注入与 Docker 构建。
- 新增 `docs/real-provider-e2e-matrix.md`，说明已有 opt-in 真实端点测试的环境变量、单测试执行命令与凭据安全要求。该矩阵用于下一阶段在具备授权的用户环境执行；没有实际凭据时继续标记未测。


## 2026-10-29：Provider 列表重复项修复与接口级回归

- 根因位于服务端列表合并：部署配置 Provider 与旧版本遗留的同 ID Web 记录被分别加入响应，造成同配置双行；不属于常规前端提交后重复追加，因为新增成功后 UI 会重新拉取并替换列表状态。
- 修复 `internal/server/admin_providers_http.go`：以规范化 Provider ID 去重，优先显示部署记录；仅显式接管后的 `SourceOverride` 记录覆盖部署行。遗留冲突记录不从持久化存储删除，避免未经用户确认的数据丢失。
- 函数回归测试覆盖遗留冲突、显式接管及已删除覆盖三种状态。提交 `502e964114d094cbd0adede6b1d3da5ec5260ec5` 进一步增加 `GET /api/admin/providers` Handler 测试，验证会话鉴权、唯一列表和 API Key 掩码。
- 代码可证明列表合并缺陷存在，但目前没有读取用户生产实例的持久化数据，因此不能断言生产环境中的重复行必然来自某一条具体数据库记录；该部分仍需在部署环境核对。
