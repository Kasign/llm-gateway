# LLM Gateway 未关闭问题与后续验收台账

> 更新日期：2026-10-26。此文件是问题统一入口。发现问题先登记、分级并定义验收条件，按优先级集中解决；关闭事项时附 commit、CI 运行与可复查结果。状态“待 PR #5 合并”表示自动化验证已通过，但修复尚未进入 `dev` 基线。

## 当前基线

- `dev` HEAD：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。
- PR #5：[Gateway TCP 连接失败与恢复](https://github.com/Kasign/llm-gateway/pull/5)，Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232`，状态 Open / 未合并。
- PR #5 最终 CI：[Actions #156](https://github.com/Kasign/llm-gateway/actions/runs/39939567499)，结论 success，26 个步骤全部成功；代码修复提交的 Actions #155 同样全量通过。
- PR #7（[远程 Provider 并发上限](https://github.com/Kasign/llm-gateway/pull/7)）当前分支基线 `6b82d47ddeb4c7ff19a430749caa530eb95a5cab`，最新已通过 [Actions #159](https://github.com/Kasign/llm-gateway/actions/runs/39944573967)；PR 仍依赖 PR #5，尚未合并。
- #155/#156 的数据源是本机 loopback Mock + CI Redis/MariaDB。不能据此宣称真实供应商 E2E、公网链路恢复或生产容量已经验收。

## P1：可靠性与可观测性

| 编号 | 问题 / 缺口 | 当前状态 | 统一解决方向 | 关闭条件 |
|---|---|---|---|---|
| GW-REL-001 | DNS NXDOMAIN/解析超时的错误映射和脱敏有测试，但尚未从真实 Gateway Handler 走到 MariaDB，明确断言 `upstream_http_status IS NULL`。 | 待补专项集成测试 | 用可控 DNS Resolver 注入解析失败，经过真实 Server Handler、审计 Writer 与 MariaDB 查询，不依赖公网 DNS。 | Gateway 返回安全 502/504；所有测试记录都无上游状态；没有 DNS 名称、Resolver 诊断或凭据泄漏；race 与完整 CI 通过。 |
| GW-REL-002 | 已收到响应头/状态码后上游断流、RST 或流式正文中断后的记录语义与客户端终止事件未由完整 MariaDB 集成测试覆盖。 | 待补专项测试 | 分别模拟收到响应头前失败、收到 200 响应头后中断、收到非 2xx 后正文读取失败；区分 Gateway 已提交状态和真实上游状态。 | 每条场景的客户端最终行为、`http_status`、`upstream_http_status`、错误码及审计记录均有精确断言。 |
| GW-REL-003 | TCP 拒绝后同端口恢复已在 PR #5 Actions #155/#156 通过，但改动尚未合入 `dev`。 | 等待 PR #5 合并 | 保持 PR #5 独立；不要重复实现或与无关功能混合。 | PR #5 合并后确认 `dev` 包含该提交，且后续 `dev` 完整 CI 通过。 |
| GW-OBS-001 | 审计队列已有有界容量、关键任务预留及 dropped 计数，但此前缺少统一只读指标接口。 | 本次已实现，待完整 CI 验收 | 原子计数与低成本快照暴露队列深度、容量、成功/失败、按原因丢弃、累计排队时延/最大排队时延和累计 drop rate；管理 API 不返回正文、Key 或原始错误。 | 高并发与满载测试通过；指标与队列行为相符；无同步数据库查询进入推理热路径；race、go vet 与完整 CI 通过。 |
| GW-OBS-002 | 请求日志 / Payload 的数据库保留期限、自动清理策略和清理任务运行指标还需要明确。 | 待设计 | 先盘点当前迁移、Payload 开关与部署数据存储，再定 retention 配置、清理批次、索引及避免长事务的策略；不得未授权删除现有数据。 | 明确默认保留期、配置兼容、清理边界和可恢复性；覆盖到期/未到期、分页批次与并发写入测试。 |
| GW-OBS-003 | 管理台轮询对 DB/CPU/推理热路径的影响仍需实测；短期缓存不等于完整负载证明。 | 待基准 | 对照无轮询、常态轮询和高频轮询，测量 DB 查询量、延迟分位数、CPU 与缓存命中率。 | 同一 Go 版本和相近 Runner 下重复采样；定义预算与刷新频率；不凭单次抖动设置阈值。 |
| GW-NET-001 | TLS 握手中断、连接池长时间陈旧、跨网络 DNS/路由恢复和公网代理链路没有 CI 等价覆盖。 | 依赖专项环境 | 先写确定性本地 TLS/RST 测试，再规划用户控制的公网验收；不把 loopback 测试命名为供应商 E2E。 | 每种网络故障类型有独立测试定义和结果；公网项附环境、时间与安全脱敏日志。 |

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
| GW-PROV-UI-001 | Web 管理台尚不能完整新增/编辑多个远程上游 Provider，且无法通过 `.env` 统一预置多家服务配置。 | 已纳入计划，尚未实施 | 管理页支持新增、编辑、启用/停用、删除、连通性测试与模型映射；支持 `.env` 预置；定义环境配置、数据库持久化和 Web 修改之间的优先级/重启语义；密钥写入后只返回掩码与配置状态，不回显明文。 | 配置优先级、密钥保护、URL/SSRF 校验、连通性测试、启停与删除、配置重载/回滚和多 Provider 隔离均有自动化测试；UI/后端契约及完整 CI 通过。 |
| GW-AGENT-001 | 同一 Agent 内多模型别名映射、Agent 级不同 Key/权限策略和配置热更新尚未实现。 | 待产品需求确认 | 保持 Agent 固定 Provider/默认模型的现有契约；需求明确之前不擅自增加权限与路由复杂性。 | 配置模型、优先级、热更新一致性和多 Agent 隔离均有测试。 |
| GW-API-001 | API/配置/数据库契约盘点已建立，但尚未维护完整机器可读 OpenAPI 及自动差异检查。 | 待规划 | 先定稳定的公开/管理 API、鉴权、请求/响应及错误模式，再逐模块维护契约。 | OpenAPI 与 Handler、前端客户端及 CI 契约测试一致。 |

## 本轮实施规则

1. 远程 Provider 并发上限已在 PR #7 完成 CI 验收；当前推进 `GW-OBS-001`（审计指标），随后按优先级补齐传输故障集成测试，并设计 `GW-PROV-UI-001`（Web 新增/管理 Provider 与 `.env` 预置）。
2. 每次开发前核实 HEAD、PR 和 CI；不要依据过期会话状态修改远程代码。
3. 按完整功能组提交中文 Conventional Commit；CI 失败必须查看日志并修复，不降低断言。
4. 真实 E2E 仅在供应商凭据与预算明确可用时执行；Mock CI 不能替代真实端点验收。
5. “已完成”必须说明验证层级：单测、Mock HTTP、Gateway + Redis/MariaDB 集成、真实供应商 E2E 或生产部署验收。


## 2026-10-23 执行记录：并发上限功能与基线依赖

- PR #6（基于旧 `dev` 的第一版）由 Actions #157 暴露编译依赖：旧 Core 没有 `UpstreamStatus` 字段。PR #6 已关闭、未合并；没有删除断言。
- 当前 PR #7 基于 PR #5 的已通过 Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232` 堆叠，最终功能 Head `dd54cf576ba4a80e7c5d1f2dd0bb3be95a776b4b` 在 [Actions #158](https://github.com/Kasign/llm-gateway/actions/runs/39943828510) 全量通过。
- 2026-10-26 继续工作前核对：PR #5 和 PR #7 仍为 Open/未合并；PR #7 上一次完整 CI 为 Actions #159（success）。新代码提交之后必须以新 Head 的 Actions 结果重新验收。
- `GW-OBS-001` 本次实现审计 Writer 只读指标快照和管理员 GET API：队列深度/容量、入队与丢弃计数、drop rate、成功/失败计数、排队时延，不查询数据库。当前提交仍需等待新 Head 的 race、go vet、完整 CI 结果。
- 新增 `GW-PROV-UI-001` 规划：Web 端新增/编辑/启停/删除/测试远程 Provider，并可在 `.env` 预置服务；安全凭据返回、环境配置与数据库设置优先级、热更新/重启行为属于实施前必须定义的契约。
