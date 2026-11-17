# 文档目录与维护规则

本文档是仓库文档导航。各主题的细节只在对应的权威文档维护；其他文档通过链接引用，不复制同一份规则或任务清单。

## 1. 推荐阅读顺序

1. [项目 README](../README.md)：产品定位、当前能力、快速启动。
2. [技术架构](architecture.md)：已实现的系统边界、推理主流程和组件职责。
3. [配置说明](../配置说明.md) 与 [配置来源矩阵](configuration-matrix.md)：部署参数、配置来源、优先级和生效时机。
4. [API / 配置 / 数据库契约](api-config-db-contracts.md) 与 [OpenAPI](openapi.yaml)：HTTP 行为和机器可读接口契约。
5. [未关闭问题与后续验收](known-issues-and-followups.md)：当前缺陷、风险和运维验收缺口。
6. [产品能力扩展规划](product-capabilities-roadmap.md)：工具能力、本地模型分析、免费模型聚合的唯一任务拆解。

## 2. 文档权威范围

| 文档 | 唯一负责的内容 | 不应重复维护的内容 |
|---|---|---|
| [architecture.md](architecture.md) | 已实现架构、推理流程、依赖方向 | 产品任务状态、完整 API 字段表 |
| [gateway-technical-guidance.md](gateway-technical-guidance.md) | 技术原则、安全边界、历史技术 Review 与仍有效的实现注意事项 | 当前功能路线、重复的架构总览、旧 CI 状态 |
| [api-config-db-contracts.md](api-config-db-contracts.md) | 当前 HTTP 路由、请求/响应、配置和数据库契约 | 产品路线、部署操作步骤 |
| [openapi.yaml](openapi.yaml) | 机器可读 HTTP 契约 | 手写的第二份接口 Schema |
| [configuration-matrix.md](configuration-matrix.md) | 配置项来源、优先级、生效时机 | 重复的部署教程 |
| [../配置说明.md](../配置说明.md) | 用户实际配置步骤与字段解释 | 内部实现细节 |
| [../README-1PANEL.md](../README-1PANEL.md) | 1Panel / Docker 部署步骤 | 配置项完整字典 |
| [known-issues-and-followups.md](known-issues-and-followups.md) | 当前未关闭问题、风险、关闭证据 | 已完成事项的重复实施方案 |
| [product-capabilities-roadmap.md](product-capabilities-roadmap.md) | 新产品能力的唯一任务 ID、架构方案、验收条件 | 当前缺陷台账、已实现接口契约 |
| [production-acceptance-checklist.md](production-acceptance-checklist.md) | 生产环境部署验收步骤和结果模板 | CI 单元测试清单 |
| [real-provider-e2e-matrix.md](real-provider-e2e-matrix.md) | 真实 Provider 的 opt-in E2E 操作与结果 | Mock 测试结果 |
| [token-usage-dashboard.md](token-usage-dashboard.md) | Token 统计口径和页面验收 | 费用核算方案 |
| [admin-state.md](admin-state.md) | 管理台 AdminState 数据源、TTL 和刷新规则 | 整体性能基准 |
| [admin-polling-performance-plan.md](admin-polling-performance-plan.md) | 管理台轮询的生产性能测量方法 | 已实现的状态接口定义 |
| [web-ui-style-interaction-review.md](web-ui-style-interaction-review.md) | 前端样式与交互 Review 历史及其后续记录 | 当前后端功能缺陷 |
| [cost-accounting-plan.md](cost-accounting-plan.md) | 已暂停的费用核算历史方案与保留边界 | 当前 Token 用量统计 |
| [refactor-implementation-plan.md](refactor-implementation-plan.md) | 历史重构过程记录；不作为当前任务来源 | 当前架构定义、当前待办 |

## 3. 当前任务来源

- 新功能开发：只从 [产品能力扩展规划](product-capabilities-roadmap.md) 读取任务 ID，按工具能力 → 本地模型分析 → 免费模型聚合推进。
- 已有功能缺陷与可靠性：只从 [未关闭问题台账](known-issues-and-followups.md) 的当前问题表确认是否仍未关闭；历史追加记录不自动构成待办。
- 生产环境验证：按 [生产部署验收清单](production-acceptance-checklist.md) 和 [真实 Provider E2E 矩阵](real-provider-e2e-matrix.md) 记录，不能用 CI/Mock 结果替代。
- API 变更：先核对实际 Handler 与 [OpenAPI](openapi.yaml)，再同步更新 [API 契约盘点](api-config-db-contracts.md) 和契约门禁。

## 4. 状态定义

| 状态 | 含义 |
|---|---|
| 未开始 | 已确认属于当前范围，尚未执行 |
| 进行中 | 已开始，尚未满足关闭条件 |
| 已完成 | 实现和对应自动化验收已完成；若需生产验证，生产状态仍单独记录 |
| 阻塞 | 有明确依赖或决策阻塞，必须写出原因 |
| 暂缓 | 已明确不在当前范围，不应继续按待办推进 |
| 未验证 | 有实现或测试代码，但缺少对应真实环境证据 |

不要将“文档中有方案”“测试代码存在”“CI 默认跳过”描述为功能已验收。

## 5. 文档更新规则

1. 修改行为时，同步更新对应权威文档；不在多个文档重复维护完整字段、参数或任务状态。
2. 新增 API 以 Handler 实际行为为准，更新 OpenAPI、契约门禁和契约盘点。
3. 配置项更新配置结构、示例模板、Compose 环境白名单（需要注入时）、配置矩阵和用户配置说明。
4. 任务关闭必须记录 commit、CI/测试结果和真实环境证据；缺少真实环境时标记未验证。
5. 历史记录可以保留，但必须标明历史基线与日期；过期的“进行中/待运行”不得覆盖当前状态。
6. 产品定位、模型选择语义和能力优先级以 README 与产品能力扩展规划为准；技术指导中的旧设计建议不得推翻已确认的产品决策。
