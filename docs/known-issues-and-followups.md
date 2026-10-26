# LLM Gateway 未关闭问题与后续验收台账

> 更新日期：2026-10-26。此文件是问题统一入口。发现问题先登记、分级并定义验收条件，按优先级集中解决；关闭事项时附 commit、CI 运行与可复查结果。状态“待 PR #5 合并”表示自动化验证已通过，但修复尚未进入 `dev` 基线。

## 当前基线

- `dev` HEAD：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。
- PR #5：[Gateway TCP 连接失败与恢复](https://github.com/Kasign/llm-gateway/pull/5)，Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232`，状态 Open / 未合并。
- PR #5 最终 CI：[Actions #156](https://github.com/Kasign/llm-gateway/actions/runs/39939567499)，结论 success，26 个步骤全部成功；代码修复提交的 Actions #155 同样全量通过。
- PR #7（[远程 Provider 并发上限](https://github.com/Kasign/llm-gateway/pull/7)）最近一次已验证的代码 Head 为 `0647b6aeef8c01f68d2a783989369c36e511293e`，对应 [Actions #166](https://github.com/Kasign/llm-gateway/actions/runs/40259993963)，32/32 步骤全部成功；PR 仍依赖 PR #5，尚未合并。
- #155/#156 的数据源是本机 loopback Mock + CI Redis/MariaDB。不能据此宣称真实供应商 E2E、公网链路恢复或生产容量已经验收。

## P1：可靠性与可观测性

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-REL-001 | DNS NXDOMAIN/解析超时此前只覆盖 Provider 层错误映射；现已补齐 Gateway Handler + Redis + MariaDB 专项集成测试。 | Actions #163 全量验收通过 | 使用注入 Resolver 返回确定性 NXDOMAIN/timeout，验证 Gateway 安全响应与审计行的 Gateway 状态/NULL 上游状态；不依赖公网 DNS。 | NXDOMAIN=502、Resolver timeout=504；两条审计记录 `upstream_http_status IS NULL`（2/2）；不泄漏主机名、Resolver 诊断或凭据；Actions #163 全量 CI 通过。 |
| GW-REL-002 | 上游已提交 HTTP 200 和部分 SSE 后发生 TCP RST 的行为，原先缺少完整 MariaDB 集成测试。 | Actions #164 全量验收通过 | 在 Gateway Handler + Redis + MariaDB 集成测试中，上游发送 200 与部分 SSE 后触发 TCP RST；验证已提交状态不可逆及审计一致性。 | 客户端 HTTP=200、保留部分 SSE 并追加安全 `event: error`；审计 `http_status=200`、`upstream_http_status=200`、`status=error`、`error_code=provider_upstream`；无凭据/底层网络诊断泄漏；Actions #164 全量 CI 通过。真实供应商 E2E 仍未覆盖。 |
| GW-REL-003 | TCP 拒绝后同端口恢复已在 PR #5 Actions #155/#156 通过，但改动尚未合入 `dev`。 | 等待 PR #5 合并 | 保持 PR #5 独立；不要重复实现或与无关功能混合。 | PR #5 合并后确认 `dev` 包含该提交，且后续 `dev` 完整 CI 通过。 |
| GW-OBS-001 | 审计队列已有有界容量、关键任务预留及 dropped 计数。 | 实现完成；Actions #162（Head `dfbc2955`）全量通过 | 原子计数与低成本快照暴露队列深度、容量、成功/失败、按原因丢弃、累计排队时延/最大排队时延和累计 drop rate；管理员 GET API 不查询数据库，不返回正文、Key 或原始错误。 | Actions #162：单测、MariaDB 审计积压、Gateway 负载/故障注入/TCP 恢复、race、性能基线、go vet、Go 构建与前端 bundle 均通过。 |
| GW-OBS-002 | 请求日志 / Payload 的数据库保留期限、自动清理策略和清理任务运行指标还需要明确。 | 待设计 | 先盘点当前迁移、Payload 开关与部署数据存储，再定 retention 配置、清理批次、索引及避免长事务的策略；不得未授权删除现有数据。 | 明确默认保留期、配置兼容、清理边界和可恢复性；覆盖到期/未到期、分页批次与并发写入测试。 |
| GW-OBS-003 | 管理台轮询对 DB/CPU/推理热路径的影响仍需实测；短期缓存不等于完整负载证明。 | 待基准 | 对照无轮询、常态轮询和高频轮询，测量 DB 查询量、延迟分位数、CPU 与缓存命中率。 | 同一 Go 版本和相近 Runner 下重复采样；定义预算与刷新频率；不凭单次抖动设置阈值。 |
| GW-NET-001 | 连接池长时间陈旧、跨网络 DNS/路由恢复和公网代理链路仍没有 CI 等价覆盖。 | 本地 TLS 握手中断用例已由 Actions #166 验收通过；其余依赖专项环境 | 本地 TCP Listener 在 TLS 握手前关闭连接，Provider 映射为安全 502、上游 HTTP 状态为空；专属步骤、race、完整 CI 均通过。后续补连接池陈旧连接与重连；公网反代/路由由用户可控环境单独验收，不把 loopback 测试称作供应商 E2E。 | TLS 用例：Actions #166 32/32 步骤成功；剩余网络类型有独立结果；公网项附环境、时间与脱敏日志。 |

## P1：运行治理

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-GOV-001 | 远程 Provider 缺少按 Provider 独立配置的并发上限和一致的过载语义。 | Actions #158 已通过；等待 PR #5 合并后重定向 base 复验 | 新增 `max_concurrent_requests`（默认值与上限均为 64）与 fail-fast 503；过载时不发起上游请求，依赖 PR #5 的 `UpstreamStatus` 契约保证上游状态为空。流式已提交时追加安全 SSE error。 | PR #7 的单测、配置范围、race 和完整 CI 已通过；PR #5 合并、PR #7 base 切回 `dev` 后再次完整 CI 通过即关闭。 |
| GW-GOV-002 | 当前 RPM 主要按调用方 Key 进行固定窗口计数，尚未提供远程 Provider 独立的并发/速率组合或 Token 预算。 | 待设计 | 在 GW-GOV-001 完成后，依据真实需求决定是否增加 Provider RPM、令牌预算和排队策略；不要与调用方 Key RPM 混为一谈。 | 有明确优先级、失败策略、Redis 不可用行为与多 Agent 隔离测试。 |
| GW-GOV-003 | 自动重试/故障转移尚未实现。 | 明确暂缓，非当前缺陷 | 定义幂等性、已提交响应判定、Provider Retry-After、预算与重复请求语义；SSE 开始输出后不得换 Provider 产生第二份答案。 | 经架构 Review 后独立决策；未批准前不启用自动切换。 |

## P2：外部兼容与产品增强

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-E2E-001 | OpenAI-compatible Chat、Responses 与 Anthropic Messages 的 opt-in 真实端点用例存在，但 CI 默认跳过，没有实际调用真实供应商。 | 待授权环境验收 | 在用户控制的安全环境提供 HTTPS BaseURL、Provider 专用 Key 和模型名；确认计费额度后执行短请求；禁止将 Key 写入仓库、日志与 Artifact。 | 记录供应商、模型、端点、日期、状态/协议核验结果与脱敏日志；开发 CI 默认不强制联网。 |
| GW-E2E-002 | 真实供应商各模型的工具调用、结构化输出、Reasoning、视觉、错误封套、真实限流与余额不足行为并未全部验证。 | 部分依赖供应商 | 按 Provider × API × 能力建立兼容矩阵，只验证有实际需求的组合。 | 每个矩阵单元注明通过、失败、不支持或未测；一家通过不代表所有厂商通过。 |
| GW-PROV-UI-001 | Web 管理台尚不能完整新增/编辑多个远程上游 Provider，且无法通过 `.env` 统一预置多家服务配置。 | 已纳入阶段 4 计划，设计已细化，尚未实施 | 环境预置同时兼容现有 YAML + `${ENV}` 和 `LLM_GATEWAY_PROVIDER_<ID>_*` 前缀变量；环境项只读、Web 项入库并动态生效；来源冲突拒绝覆盖；Web Key 加密持久化且 API 永不回显明文。UI 支持增删改/启停/测试连接/默认模型及映射。 | 配置来源、重复 ID、AES-GCM Key 存储、掩码/保留/清除、URL/SSRF、模型列表测试、活动 Provider 删除保护、动态 Registry 更新/回滚、重启恢复及多 Provider 隔离都有自动化测试；UI/后端契约、bundle 和完整 CI 通过。 |
| GW-AGENT-001 | 同一 Agent 内多模型别名映射、Agent 级不同 Key/权限策略和配置热更新尚未实现。 | 待产品需求确认 | 保持 Agent 固定 Provider/默认模型的现有契约；需求明确之前不擅自增加权限与路由复杂性。 | 配置模型、优先级、热更新一致性和多 Agent 隔离均有测试。 |
| GW-API-001 | API/配置/数据库契约盘点已建立，但尚未维护完整机器可读 OpenAPI 及自动差异检查。 | 待规划 | 先定稳定的公开/管理 API、鉴权、请求/响应及错误模式，再逐模块维护契约。 | OpenAPI 与 Handler、前端客户端及 CI 契约测试一致。 |

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
