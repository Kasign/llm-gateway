# LLM Gateway 未关闭问题与后续验收台账

> 更新日期：2026-10-26。此文件是问题统一入口。发现问题先登记、分级并定义验收条件，按优先级集中解决；关闭事项时附 commit、CI 运行与可复查结果。状态“待 PR #5 合并”表示自动化验证已通过，但修复尚未进入 `dev` 基线。

## 当前基线

- `dev` HEAD：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。
- PR #5：[Gateway TCP 连接失败与恢复](https://github.com/Kasign/llm-gateway/pull/5)，Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232`，状态 Open / 未合并。
- PR #5 最终 CI：[Actions #156](https://github.com/Kasign/llm-gateway/actions/runs/39939567499)，结论 success，26 个步骤全部成功；代码修复提交的 Actions #155 同样全量通过。
- PR #7（[远程 Provider 并发上限与多 Provider 管理](https://github.com/Kasign/llm-gateway/pull/7)）`6b7c839b48d4b4a92d932ab95e79c84fbea05c1b` 的完整 CI [#40266726738](https://github.com/Kasign/llm-gateway/actions/runs/40266726738) 已通过全部 32 步。后续复核发现并发上限提高应在 Registry 成功替换后再生效，已进入下一次功能修正；新提交必须再次跑完整 CI。PR 仍 Open，依赖 PR #5，尚未合并。
- #155/#156 的数据源是本机 loopback Mock + CI Redis/MariaDB。不能据此宣称真实供应商 E2E、公网链路恢复或生产容量已经验收。

## P1：可靠性与可观测性

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-REL-001 | DNS NXDOMAIN/解析超时此前只覆盖 Provider 层错误映射；现已补齐 Gateway Handler + Redis + MariaDB 专项集成测试。 | Actions #163 全量验收通过 | 使用注入 Resolver 返回确定性 NXDOMAIN/timeout，验证 Gateway 安全响应与审计行的 Gateway 状态/NULL 上游状态；不依赖公网 DNS。 | NXDOMAIN=502、Resolver timeout=504；两条审计记录 `upstream_http_status IS NULL`（2/2）；不泄漏主机名、Resolver 诊断或凭据；Actions #163 全量 CI 通过。 |
| GW-REL-002 | 上游已提交 HTTP 200 和部分 SSE 后发生 TCP RST 的行为，原先缺少完整 MariaDB 集成测试。 | Actions #164 全量验收通过 | 在 Gateway Handler + Redis + MariaDB 集成测试中，上游发送 200 与部分 SSE 后触发 TCP RST；验证已提交状态不可逆及审计一致性。 | 客户端 HTTP=200、保留部分 SSE 并追加安全 `event: error`；审计 `http_status=200`、`upstream_http_status=200`、`status=error`、`error_code=provider_upstream`；无凭据/底层网络诊断泄漏；Actions #164 全量 CI 通过。真实供应商 E2E 仍未覆盖。 |
| GW-REL-003 | TCP 拒绝后同端口恢复已在 PR #5 Actions #155/#156 通过，但改动尚未合入 `dev`。 | 等待 PR #5 合并 | 保持 PR #5 独立；不要重复实现或与无关功能混合。 | PR #5 合并后确认 `dev` 包含该提交，且后续 `dev` 完整 CI 通过。 |
| GW-OBS-001 | 审计队列已有有界容量、关键任务预留及 dropped 计数。 | 实现完成；Actions #162（Head `dfbc2955`）全量通过 | 原子计数与低成本快照暴露队列深度、容量、成功/失败、按原因丢弃、累计排队时延/最大排队时延和累计 drop rate；管理员 GET API 不查询数据库，不返回正文、Key 或原始错误。 | Actions #162：单测、MariaDB 审计积压、Gateway 负载/故障注入/TCP 恢复、race、性能基线、go vet、Go 构建与前端 bundle 均通过。 |
| GW-OBS-002 | 请求日志 / Payload 的数据库保留期限与自动清理。 | 代码已实现，待统一测试 | `logging.retention_days` 默认 0（禁用）；正数保留天数后，按 500 条默认批次、每轮最多 10 批清理已结束请求，先删除流事件和 Payload 再删除请求日志；单批独立事务并支持取消。 | MariaDB 验证到期/未到期、运行中请求、批次边界、并发写入与取消；确认清理指标/日志与失败告警后关闭。 |
| GW-OBS-003 | 管理台轮询对 DB/CPU/推理热路径的影响仍需实测；短期缓存不等于完整负载证明。 | 待基准 | 对照无轮询、常态轮询和高频轮询，测量 DB 查询量、延迟分位数、CPU 与缓存命中率。 | 同一 Go 版本和相近 Runner 下重复采样；定义预算与刷新频率；不凭单次抖动设置阈值。 |
| GW-NET-001 | TLS 握手中断、连接池陈旧连接及公网代理链路的覆盖不相同。 | TLS 握手中断由 Actions #166 验收通过；陈旧 Keep-Alive 恢复专项新增，待 CI；公网反代/路由待部署环境验收 | 区分故障注入：TLS 在握手前关闭；陈旧连接先建立成功响应，再由服务端清空空闲连接，第二次请求必须成功并建立替代 TCP 连接。公网链路不能由 loopback 代替。 | 陈旧连接测试在完整 CI/race 通过，第二次请求成功且请求/连接计数满足预期；公网项另附环境、时间与脱敏日志。 |

| GW-NET-002 | Provider Transport 复用的 Keep-Alive 连接可能被 NAT、反向代理或上游空闲超时提前关闭。 | 陈旧连接恢复用例已纳入完整 Actions；当前 Head 的 #172 运行全量通过 | 首次请求成功后关闭空闲连接；不得盲目重试可能已执行的 POST；允许当前请求映射安全 502，但下一次请求必须建立替代 TCP 连接并成功。 | 当前完整 Actions 的 race、静态检查、Go build 和 bundle 均通过；公网反代和真实供应商链路仍单独验收。 |

## P1：运行治理

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-GOV-001 | 远程 Provider 缺少按 Provider 独立配置的并发上限和一致的过载语义。 | 限流、fail-fast 503、JSON/SSE 与配置范围已由 PR #7 CI 验证；等待 PR #5 合并后重定向 base 复验 | `max_concurrent_requests` 默认/上限均为 64；过载不发起上游请求，`UpstreamStatus` 为空；流式已提交时追加安全 SSE error。热更新实例必须复用同一个 Provider 并发许可器，避免旧请求仍在执行时替换实例导致总并发超限。 | 同实例与替换实例共享并发预算；降低上限时不接受超过新预算的新请求；旧请求结束后许可正常释放；完整 CI/race 通过。PR #5 合并且 PR #7 base 回到最新 `dev` 后再次完整 CI 通过。 |
| GW-GOV-002 | Provider 级 RPM、Token 预算与排队策略尚未全部齐全。 | Provider RPM 已实现，待统一测试；Token 预算与排队仍待设计 | `requests_per_minute=0` 默认禁用；正数在请求级 ACL 之后、WarmStart/上游前按 Provider ID 使用 Redis UTC 固定分钟窗口计数。多个 API Key 和 Agent 共享 Provider 额度；超过上限返回 429，Redis 不可用时 fail-closed 返回 503。本地 llama.cpp/Ollama 不受该远程 Provider 限额影响。 | 统一测试 Redis 计数隔离、429/503 审计、动态 Web 配置更新与多 Agent 共用额度；Token 预算与排队另行评审。 |
| GW-GOV-003 | 自动重试/故障转移尚未实现。 | 明确暂缓，非当前缺陷 | 定义幂等性、已提交响应判定、Provider Retry-After、预算与重复请求语义；SSE 开始输出后不得换 Provider 产生第二份答案。 | 经架构 Review 后独立决策；未批准前不启用自动切换。 |

## P2：外部兼容与产品增强

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-E2E-001 | OpenAI-compatible Chat、Responses 与 Anthropic Messages 的 opt-in 真实端点用例存在，但 CI 默认跳过，没有实际调用真实供应商。 | 待授权环境验收 | 在用户控制的安全环境提供 HTTPS BaseURL、Provider 专用 Key 和模型名；确认计费额度后执行短请求；禁止将 Key 写入仓库、日志与 Artifact。 | 记录供应商、模型、端点、日期、状态/协议核验结果与脱敏日志；开发 CI 默认不强制联网。 |
| GW-E2E-002 | 真实供应商各模型的工具调用、结构化输出、Reasoning、视觉、错误封套、真实限流与余额不足行为并未全部验证。 | 部分依赖供应商 | 按 Provider × API × 能力建立兼容矩阵，只验证有实际需求的组合。 | 每个矩阵单元注明通过、失败、不支持或未测；一家通过不代表所有厂商通过。 |
| GW-PROV-UI-001 | Web 多 Provider 管理与 `.env` 预置。 | 活动 Provider 的删除、停用、清 Key 与保存时将 `enabled` 改为 false 均有后端保护代码；现有回归测试覆盖保护函数，但最新 PR Head 的 Actions 在单元测试阶段失败，尚未视为验收通过。 | 继续按统一测试安排验证配置来源、密钥存储、Registry 更新/回滚与活动 Provider 安全边界。 | 完整 CI 通过，并在部署环境核对密钥不回显、活动 Provider 无法被间接停用/清 Key、Registry 失败时配置回滚。 |
| GW-AGENT-001 | 同一 Agent 内多模型别名映射、Agent 级不同 Key/权限策略和配置热更新尚未实现。 | 待产品需求确认 | 保持 Agent 固定 Provider/默认模型的现有契约；需求明确之前不擅自增加权限与路由复杂性。 | 配置模型、优先级、热更新一致性和多 Agent 隔离均有测试。 |
| GW-API-001 | API/配置/数据库契约盘点已建立，但尚未维护完整机器可读 OpenAPI 及自动差异检查。 | `docs/openapi.yaml` 已覆盖健康检查、推理入口、调用方 API Key、登录会话、请求统计、请求审计、审计指标、运行时/Benchmark 的常用端点及 Web Provider 管理；仍为 partial，尚缺下载、HuggingFace/Ollama、推理参数和 Bootstrap 等端点与自动差异检查。 | 按功能层继续补齐未覆盖端点；每个端点依照 Handler、权限与实际响应核对，不猜字段。 | OpenAPI 覆盖全部公开/管理 API，认证与错误模式核实完成，且有自动差异或契约检查。 |

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

- 已为 DeepSeek/OpenAI 之外的 Provider 增加独立可选 `deploy/providers.env` 注入，解决 Compose 只显式传递两组 ID 的限制。
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
- `deploy/config.example.yaml`、`deploy/providers.env.example` 和中文配置说明补充了 RPM 样例。

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
