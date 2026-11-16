# LLM Gateway 整体 Review 与功能路线图

> Review 日期：2026-11-16
> 检查分支：`dev`
> 代码 Review 基线：`b0cfc3d371f926ad9164bf904cd0bbdda22a2975`（后续提交仅更新验收文档）
> 最新 Go CI：[43870019761](https://github.com/Kasign/llm-gateway/actions/runs/43870019761)，5/5 Job 成功
> Browser Smoke：[43869433077](https://github.com/Kasign/llm-gateway/actions/runs/43869433077)，165/165 检查通过
>
> 本文用于统一规划，不代表生产环境验收已经完成。代码级 CI、Mock/故障注入、真实 Provider E2E 和生产部署验收必须分别记录。

## 1. 总体判断

项目已经具备较完整的个人 Coding Agent Gateway 功能核心：本地与远程 Provider 接入、统一推理入口、调用方鉴权、限流与并发治理、流式转发、请求审计、管理台和部署配置都有对应实现。核心推理主流程收敛在 `internal/core`，Provider 具体转发通过 Registry/适配器接入；这条主架构应保留。

当前阶段不建议继续无差别扩展功能，也不建议一次性重写 `internal/server` 或 Provider 层。优先顺序应是：

1. 完成 1Panel / OpenResty / 公网 HTTPS / SSE / 慢模型 / 真实 Provider 的端到端验收；这是当前唯一不能由仓库 CI 替代的主阻塞。
2. 对管理台轮询做真实负载基准，确认 DB 查询量、CPU 和延迟预算；当前短期缓存与节流已有实现，但尚无生产测量。
3. 按风险增量补齐 OpenAPI 字段级契约，维持现有门禁，不追求一次性覆盖全部历史接口。
4. 继续完成低风险 UI 可访问性与视觉细节审计；不扩大功能范围。

## 2. 当前功能状态

| 功能域 | 当前判断 | 接下来真正需要做的事 |
|---|---|---|
| 推理协议入口 | 已有 Chat Completions、Completions、Responses、Anthropic Messages 等路径；不同协议仍有各自的兼容边界 | 用真实 Provider 按需验证协议、工具调用、结构化输出、Reasoning/视觉等能力，不宣称所有厂商完全等价 |
| 本地模型 | 已有 llama.cpp / Ollama、模型发现与管理、GGUF 下载和运行时状态；代码级已补齐 HF 取消/删除竞态、Ollama Pull 重启状态恢复、删除前卸载失败保护、ModelsMax=1 加载前卸载失败保护 | 在实际容器网络、Router/单模型模式和模型目录挂载下完成验收 |
| 远程 Provider | 已统一由 Web 管理台和 MariaDB 管理；Key 使用 AES-GCM 密文存储，列表不回显明文；Provider ID 冲突有后端保护与测试 | 验证生产数据兼容、密钥备份与恢复；后续只修复可复现缺陷 |
| 请求治理 | 已实现调用方 API Key、模型 ACL、RPM、Provider 并发、可选 FIFO 队列、每日 Token 预算 | 真实用量、慢上游、实际客户端取消和反代超时仍需端到端测试；队列目前是单进程协调 |
| 请求审计 | 已有异步审计、有界队列、请求详情、流事件和保留策略 | 通过生产样本验证数据增长、保留策略与管理台轮询的实际影响 |
| 管理员身份 | Web Session 与管理员 API Key 已分离；管理员 API Key 的服务端权限限定于 `/api/admin/keys` 路径树；固定默认密码已移除，密码保护对无效哈希失败关闭并有回归测试 | 后续评估 Web Session 在前端 localStorage 的存储风险；真实 1Panel 环境仍需验收 |
| API/配置契约 | Token 用量、管理员 API Key、Ollama、Runtime、推理参数和 Benchmark 等高风险接口已完成 Handler/DTO/OpenAPI 对账与门禁 | OpenAPI 整体仍为 partial；后续按风险增量补齐，不追求一轮覆盖所有历史接口 |
| 管理台体验 | 近期 Browser Smoke 已覆盖关键桌面/窄屏交互；主要视觉风格相对一致 | 移动导航、请求表格信息密度、Provider 模板与 Key 的交互说明、剩余可访问性细节按优先级渐进处理 |
| 生产与容量验收 | CI 当前通过 | 真实 Provider、公网 HTTPS/SSE、1Panel/OpenResty、真实慢模型和轮询负载仍未完成环境验收 |

参考文档：
- 架构与推理流程：`docs/architecture.md`
- 未关闭问题台账：`docs/known-issues-and-followups.md`
- 配置来源：`docs/configuration-matrix.md`
- API/数据库契约：`docs/api-config-db-contracts.md`
- 生产验收：`docs/production-acceptance-checklist.md`
- 真实 Provider 矩阵：`docs/real-provider-e2e-matrix.md`
- 管理台轮询性能方案：`docs/admin-polling-performance-plan.md`
- UI Review：`docs/web-ui-style-interaction-review.md`

## 3. Review 发现与优先级

### P0：密码保护必须失败关闭

**发现**

- 已修复：部署模板不再提供固定默认密码；初始密码哈希必须由部署者自行配置。
- 已修复：`internal/server/web_login.go` 保留显式密码保护开关；`WebLoginConfigured(...)` 现在检查哈希结构，缺失或结构损坏时登录失败关闭。
- `internal/server/admin_auth_http.go` 的登录逻辑在 `PasswordEnabled=false` 时允许不校验密码并签发 Web Session。主动关闭保护是已支持的功能；问题在于配置错误不应隐式等价于管理员主动选择关闭保护。

**风险**

新部署忘记更换默认凭据，或配置/环境变量缺失导致密码保护意外关闭时，若管理台经公网反代暴露，可能被未授权访问者取得管理会话。

**解决要求**

1. 已完成：删除可直接使用的固定默认密码；部署首次启动必须由管理员生成并配置自己的密码哈希。
2. 已完成实现与回归测试：密码保护启用但哈希缺失或结构无效时，阻止建立会话并返回 `503 login_not_configured`；不再隐式降级为无密码登录。
3. 只有管理员明确将密码保护开关设置为关闭，才允许进入现有无密码会话流程；文档持续警告该模式仅适合可信网络。
4. 保留现有数据库优先、密码修改、会话版本失效和 `LLM_GATEWAY_WEB_LOGIN_RESET_FROM_ENV` 恢复流程，不能通过删表、清库或重置其他设置来补救。
5. 已完成：Compose/.env 示例不含可直接登录的弱默认密码；Compose 对空哈希配置明确报错，不回显实际凭据。

**验收条件**

- 全新数据库、无密码哈希、保护开关为 true：不能创建会话。
- 配置了合法自定义哈希：正确密码登录成功，错误密码返回 401，并继续受限流保护。
- 保护开关显式为 false：按现有设计创建会话；部署指南明确该状态的网络暴露风险。
- 修改密码后旧密码失效，重启后状态持久化；临时恢复开关使用后能按步骤关闭。
- 在真实 1Panel 反代环境再次确认管理台未配置凭据时无法被无密码进入。

### P1：生产端到端验收

当前 CI 的主要价值是代码行为、Mock、竞态、故障注入和构建回归；它不能证明真实供应商计费行为、公网路由、真实模型生成速度和客户端/反代之间的时限组合。

按 `docs/production-acceptance-checklist.md` 一次性完成：

1. 记录当前部署提交、镜像、Compose 配置、数据库备份和回滚方式。
2. 检查 Gateway/MariaDB/Redis 状态、Compose 插值、容器网络和模型目录挂载。
3. 验证控制台密码保护、改密、登出、重启持久化和恢复开关关闭。
4. 验证 Provider Key 掩码、真实授权 Provider 短请求、模型映射及本地模型发现/加载状态。
5. 从公网 HTTPS 发起非流式和 SSE 请求，检查首段输出、慢模型首 Token 等待、Gateway/客户端/OpenResty 各自超时、断开取消与上游恢复。
6. 验证上游失败、认证失败、流式中断的调用记录状态与敏感信息脱敏，检查回滚流程。

真实供应商 E2E 使用 `docs/real-provider-e2e-matrix.md`。先执行最少的 Chat Completions、Responses、Anthropic Messages 必需组合；只有产品实际需要时再测试工具调用、结构化输出、Reasoning、视觉和厂商限流。密钥不写入仓库、日志或 Artifact。默认 CI 继续不访问付费服务。

### P1：配置/权限边界继续保持单一语义

- 远程 Provider 唯一权威来源是 Web 管理台和 MariaDB；不恢复 YAML / .env Provider 预置路线。
- 管理员 API Key 只用于 `/api/admin/keys` 及其子路径；控制台其余管理 API 使用 Web Session。OpenAPI 与服务端 allowlist 均应保持这一边界。
- 调用方 API Key、管理员 API Key、Web Session、Provider 上游 Key 是四类不同凭据；不能共享用途、混用存储或出现在日志/错误响应里。
- Provider 创建的重复 ID 由后端拒绝，不能回退成 upsert 覆盖；编辑、清 Key、停用和删除活动 Provider 必须继续受保护。
- URL、HTTPS、私网访问例外、禁止重定向、响应体上限、超时和 Provider 预算规则继续通过单测和契约门禁锁定。

统一认证口径：管理员 API Key 仅管理调用方服务密钥（`/api/admin/keys` 及子路径）；其他控制台管理 API 一律使用 Web Session。调用方推理 API Key 和 Provider 上游 Key 也各自限定在独立用途。新增接口必须同步更新 Handler、OpenAPI、契约检查与文档。

### P1：OpenAPI 的剩余工作采用风险驱动

现有脚本已覆盖多项高风险契约，包括认证范围、Provider 冲突、请求列表/详情、分析 DTO、保留状态和关键响应。但整体 OpenAPI 仍是分阶段覆盖，不应宣称与所有 Handler 完全等价。

下一步仅按以下顺序补齐：
1. 先覆盖凭据变更、Provider 修改/删除、Key 轮换/禁用、取消请求等高风险操作的鉴权方式、状态码和错误封套。
2. 再覆盖模型下载/取消、模型运行时、推理参数、Ollama 管理和 Benchmark 的稳定请求/响应字段。
3. 动态快照或第三方上游原始结构只有在确实动态时才用宽松 Schema；稳定 JSON DTO 应有明确 Schema。
4. 门禁应校验 Handler/DTO 与 Schema 的稳定字段、状态码和认证路径，不能只校验文档文本里出现某个词。

不实施“一次性猜测所有接口字段”的全量改写；OpenAPI 标记为 partial，直到剩余路由逐项对账完毕。

### P2：容量与管理台轮询性能

`docs/admin-polling-performance-plan.md` 已给出对照方案，按无轮询、默认轮询和约 2 倍频率分组，每组至少重复 3 次；预热 2 分钟，采样 10 分钟。

要记录管理接口请求量、端点延迟分位数、5xx、Gateway CPU/RSS、MariaDB 查询/慢查询、Redis 命令量，以及固定 Mock 推理请求的 TTFT、总耗时和错误率。先看重复样本趋势，再决定是否需要降低频率、合并端点或加缓存；没有数据前不添加新的缓存层或抽象。

### P2：数据库迁移可追踪性

目前迁移由 `001_init.sql`、`ensureColumns` 和重复列/索引错误兼容逻辑构成，没有独立的有序 `schema_migrations` 版本账本；SQL 文件按分号拆分也不适合复杂语句。

这不是要求立即重构数据库系统。只有在新增下一批需要数据转换、删除或重命名字段的迁移前，再引入有序迁移版本记录。实施时必须提供：
- 数据库备份及恢复步骤；
- 对老版本、空库和中间版本的迁移测试；
- 失败时可定位的版本/语句信息；
- 避免将真实 SQL 参数、密码或业务正文打入日志；
- 不在迁移前自动删除旧数据或重置用户配置。

### P2：前端体验与凭据会话安全评估

UI 样式无需整体换肤。按现有 `docs/web-ui-style-interaction-review.md` 渐进执行：
1. 移动端主导航收敛为抽屉或单行导航，减少首屏高度。
2. 明确切换 Provider 模板时 API Key 是否保留，避免把上家凭据错误用于新厂商。
3. 提高调用记录表格信息层级和窄屏可读性。
4. 对剩余动态叠色、键盘可访问性、空/加载/错误状态做增量浏览器回归。

当前 Web Session 和服务密钥页面的管理员 Key 使用 localStorage，带来 XSS 场景下的凭据暴露风险。这属于安全设计评估项，不代表已发现可利用的 XSS。后续应评估 CSP/输入输出转义与会话令牌存储的组合，若迁移到 HttpOnly Cookie，必须一并设计 SameSite/CSRF 防护和旧客户端兼容，不应单独换存储方式后就宣称安全。

### P3：架构与代码维护

- `internal/server` 同时承载大量路由、鉴权、模型管理、请求审计和运行时协调，体量偏大；`internal/provider/openai_compatible.go` 也同时包含传输、协议错误处理和并发队列。后续以实际改动为入口分模块，不做大规模目录迁移。
- Provider 管理代码存在长行和压缩语句，未来触及对应功能时按文件局部规范化；不做纯格式化的大范围改动。
- 目前请求级队列是进程内协调。只有明确需要多副本部署时，才评估 Redis 分布式并发/队列协调，并定义可用性、公平性和故障时策略。
- 自动重试/故障转移仍然暂缓：未确认请求未执行、未提交响应和计费语义之前，不重放推理 POST；SSE 已提交后禁止切换 Provider 续写。
- Agent 级独立 Key ACL、多模型别名和热更新仍待明确需求，不因“可能有用”先行扩充权限模型。

## 4. 整体路线图

| 阶段 | 优先级 | 工作范围 | 完成定义 |
|---|---|---|---|
| 阶段 0：认证安全收口 | P0 | **实现与文档已完成，CI 已通过**：移除固定默认密码、无效哈希失败关闭、部署模板与恢复指引、回归测试 | CI 已验证缺失/损坏哈希与登录模式的核心逻辑；真实 1Panel 反代验收仍列入阶段 1 |
| 阶段 1：生产闭环 | P1 | **当前下一阶段**：一次性完成 1Panel/OpenResty/慢模型/取消/恢复/真实 Provider 最小矩阵 | `docs/production-acceptance-checklist.md` 仍全部为未测；需要真实部署环境、真实 Provider 与公网 HTTPS/SSE 证据，不能由 Mock/CI 替代 |
| 阶段 2：接口与配置收口 | P1 | 更正权限说明；按实际风险补 OpenAPI Schema/状态码/认证范围；保留 Provider 单一来源与冲突语义 | 高风险门禁通过；一般接口清单仍如实标注 partial |
| 阶段 3：实测与运维能力 | P2 | 管理台轮询基准；补充容量/指标预算；为需要数据迁移的后续变更设计有序 schema 版本 | 有可复现数据与报告后再决定优化；迁移方案具有备份、失败恢复与兼容测试 |
| 阶段 4：体验完善 | P2 | 移动导航、Provider 模板 Key 提示、调用表格、剩余无障碍/状态反馈 | Browser Smoke 增量覆盖主要视口、操作和状态 |
| 阶段 5：按需求演进 | P3 | 模块化维护、会话存储加固、多副本协调、Agent 权限、重试/故障转移评估 | 先定义实际需求和协议/安全语义；未批准不进入开发 |

## 5. 开发与验收规则

1. 每个提交按一个可验证功能组组织，使用中文提交说明；文档修正不伪装成实现完成。
2. 实现变更以新 HEAD 对应的 CI 为准；新提交覆盖旧运行时，必须记录最终成功的那一轮。
3. 最新 `dev` 的 Go CI 5/5 Job 已通过（`43849798940`）；最新代码节点 `a5c8a712d44830146f2cccfa4a7d0be926e980a8` 的 Browser Smoke 也已通过（`43849359250`）。后续不要反复重做已验收的契约、下载和模型生命周期回归；只有出现新的复现证据才扩大实现范围。
4. 真实 Provider、生产部署和负载测试分别记录，不把 Mock 测试、静态契约或截图验收替代真实环境结论。
5. 不直接修改 `main`，不自动合并 PR；本路线图只规划后续任务，不授权重写或引入延后功能。
6. 每轮优先解决一个 P0/P1 风险或一个真实、独立的产品问题；若只有生产环境才能验证，完善执行材料后等待环境证据，不重复空转审查。

## 6. 本次检查基线

- 部署代码基线：`6d02eb45684db188c2a5e1427cffe8a10fc6c3fc`（随后仅有验收文档更新）
- Go CI：[43866482448](https://github.com/Kasign/llm-gateway/actions/runs/43866482448)，5/5 Job 成功，包含默认 release Docker 镜像构建。
- Browser Smoke：[43866482461](https://github.com/Kasign/llm-gateway/actions/runs/43866482461)，164/164 检查通过；无页面异常、无 JS Console 错误、无静态资源加载失败。
- Browser Smoke 的 502 仅来自刻意未启动的本地模型上游，属于预期不可用场景，不是页面资源错误。
- 真实 1Panel/OpenResty/真实 Provider 验收仍未执行；代码级 CI 不代替生产环境验收。


## 2026-11-16：管理台轮询与 Ollama 目录解析 Review（进行中）

- 本轮基线：`d422456006dca934f17cc206a73196ec0a472ce6`，工作分支仅为 `dev`。
- 调用记录轮询修复已提交 `5f596290171019a19d1bab010e04a0505c2569ab`：轮询不清空旧内容；相同列表快照不重复渲染；统计最多每 60 秒刷新；增加列表/统计请求序号保护，避免过期响应覆盖新查询。
- Ollama 目录解析修复已提交 `d422456006dca934f17cc206a73196ec0a472ce6`：搜索卡片不再依赖固定 class 与属性顺序；标题/描述支持嵌套标签和实体；无可识别模型链接时明确返回解析错误；Tag 解析不再将空结果伪装成 `latest`。
- 对应 Go CI `43862999568`：Go 测试/竞态/静态检查、Docker 构建、稳态负载、故障注入均成功；“配置、API 契约与前端”因源码变更后静态部署 Bundle 尚未同步而失败。Browser Smoke `43862999564` 同样停在 Bundle 构建/同步检查，不能记为通过。
- 前端 Bundle 已由 CI 构建，待将生成的 `web/static/dist/app.js` 同步到仓库后重跑完整 CI。
- 后续：补充 catalog 来源级部分失败提示（先确认现有 API/前端契约是否有合适扩展点）；完成 Bundle 同步；统一问题台账与真实部署验收状态。

- 后续提交 `87916045fd50b11d275f590ceb83e32cbd8e0d17` 移除前端 Ollama Tag 空结果时自动生成 `latest` 的兜底；`ab9b8b16a179b2d5dcaeaf01eedfaaa536d0e203` 增加 catalog 来源级状态，部分来源失败时保留成功来源结果并在管理台显示错误，且同步 OpenAPI 与部分失败测试。


## 2026-11-16：默认 Docker 部署目标前端资源修复

- 根因已定位到镜像打包链路：`docker-compose.yml` 未指定 target，默认构建 Dockerfile 的 `release`；旧 `release` 从仓库复制 `web/static/dist`，而不是使用前面已经构建的 `webbuilder` 产物。这样源码即使修复，默认生产镜像仍可能带旧 JS。
- Dockerfile 已调整为默认 `release` 继承 `release-with-web`，保证 Compose 默认构建也使用本次源码生成的静态资源；不再要求部署前手工替换 `app.js`。
- `index.html` 的 app.js 查询版本同步更新，降低浏览器继续命中旧缓存的概率。
- Go CI 与 Browser Smoke 改为校验本次构建产物本身的语法、关键字段和浏览器行为，不再要求仓库内跟踪的静态 Bundle 必须与当前源码逐字一致；生产镜像必须从 webbuilder 取资源。
- 本修复已由 Go CI 5/5 和 Browser Smoke 164/164 验证；默认 Docker release 已成功构建。真实公网反代与生产浏览器验收仍待用户部署后确认。


## 2026-11-16：最新代码 Review 增量

- 本轮复核确认：认证保护的 P0 实现已完成，当前路线不应再把“修复默认密码/无效哈希降级”列为下一项；阶段 0 已关闭，下一阶段是生产闭环。
- Docker 默认 `release` 已继承 `release-with-web`，Compose 默认构建使用 webbuilder 生成的前端资源；该修复对应 Go CI 5/5、Browser Smoke 164/164。
- 复核调用记录轮询时发现 `loadRequests({silent:true})` 的调用方已经存在，但失败分支忽略 `silent`，网络故障时仍会重复弹 Toast。提交 `99e7567ce06f20b1d1d9d729441b8092105bc7f0` 修复；提交 `3b683b4f05cce88caf95a7911f2b3bcc349aa760` 增加回归，随后将故障注入改为 HTTP 503，避免 Chromium 对 `abort()` 的 `net::ERR_FAILED` 产生误报。最终 Browser Smoke 165/165 通过，预期 503 已被记录为预期故障，无 Console 错误。
- 当前代码 Review 未发现新的阻止部署的已证实后端逻辑缺陷；Go CI 5/5、Browser Smoke 165/165 已通过。但真实 Provider、Ollama 实际页面结构、生产反代和轮询负载仍没有真实环境证据，不能记为全部完成。
- 持续遗留项：OpenAPI 整体仍为 partial；管理台 localStorage Session 的 XSS 风险属于设计评估项；多副本全局队列/并发协调、自动故障转移、Agent 级多别名/权限均为明确暂缓或待需求，不属于本次部署阻塞。
