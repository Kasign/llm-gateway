# LLM Gateway 整体 Review 与功能路线图

> Review 日期：2026-11-16
> 规划更新：2026-11-17
> 检查分支：`dev`
> 代码验收基线：`b0cfc3d371f926ad9164bf904cd0bbdda22a2975`（后续规划提交为文档变更）
> 本文中的 CI、Browser Smoke 与生产验收状态分别记录；CI 通过不代表真实 Provider 或生产部署已验收。

## 2026-11-17：产品能力扩展路线更新

此前文档中关于“Gateway 只做本地 Coding Agent 入口”以及将按请求模型路由作为长期方向的描述，不再代表当前产品目标。当前产品定位、明确的模型选择语义、三项能力的架构设计、任务 ID、依赖关系和验收标准，以 [产品能力扩展规划](product-capabilities-roadmap.md) 为准。

新的产品能力主线按以下顺序推进：

1. **工具能力**：建立受控 Tool Registry / Runtime，先接入 SearXNG，优先实现非流式 Chat Completions 的 Gateway 内部工具调用。
2. **本地模型分析**：补齐现有请求分析的阶段定义和来源可信度，结合 llama.cpp timings 与低频资源快照，形成可重复基准。
3. **免费模型聚合**：复用现有 Provider Registry 和凭据管理，维护官方免费计划与模型能力元数据，逐个完成真实 Provider 验收。

上述工作当前均为规划任务，不代表代码已开始。具体执行顺序、非目标、任务拆解和验收条件只维护在 `docs/product-capabilities-roadmap.md`，避免在多个路线图中重复维护任务状态。

## 既有系统 Review 结论

当前分支已具备本地与远程 Provider 接入、统一推理入口、调用方鉴权、限流与并发治理、流式转发、请求审计和管理台。核心推理主流程收敛在 `internal/core`，Provider 具体转发通过 Registry/适配器接入；不建议为本轮能力扩展重写主流程或大规模拆分 `internal/server`。

现有能力与遗留风险按各主题权威文档管理；本文只保留总体结论和入口：
- 架构与推理流程：`docs/architecture.md`
- 未关闭问题台账：`docs/known-issues-and-followups.md`
- 配置来源：`docs/configuration-matrix.md`
- API/数据库契约：`docs/api-config-db-contracts.md`
- 生产验收：`docs/production-acceptance-checklist.md`
- 真实 Provider 矩阵：`docs/real-provider-e2e-matrix.md`
- 管理台轮询性能方案：`docs/admin-polling-performance-plan.md`
- 文档职责与维护规则：`docs/README.md`
- UI Review：`docs/web-ui-style-interaction-review.md`

---

