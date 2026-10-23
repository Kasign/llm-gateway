# LLM Gateway 重构实施计划与进度

> 目标分支：`dev`
> 初始计划基线：`e9e55286a4d3d2cba4d76df212280b8547065267`
> 状态：阶段 0——计划与基线核验

## 1. 重构目标

将项目演进为统一模型接入与请求治理网关，支持本地 llama.cpp、Ollama，以及后续 GPT、DeepSeek 等外部服务。重点是扩展新 Provider 时，不需要反复修改核心请求主流程；日志不能改变模型响应内容、流式顺序或业务成功与否。

## 2. 架构原则

1. `internal/core` 只负责请求流程编排，不依赖数据库、Redis、HTTP Handler 或具体厂商 SDK。
2. Provider 适配器负责协议与上游通信，不依赖管理台和数据库实现。
3. 本地模型加载、卸载、下载属于可选能力，不强迫远程 Provider 实现。
4. 日志只观察请求；不能决定业务结果、修改响应内容或重排流式输出。
5. 调用方 Gateway API Key 与厂商 API Key 分属不同凭据域。
6. 不一次性搬迁全部目录。只有某个阶段确实需要时才调整结构。
7. 中文文档说明目标、边界、运维和设计原因；公共接口与复杂并发逻辑添加必要中文注释，不添加逐行废话注释。

建议目标目录（按阶段逐步形成，不要求一次性迁移）：

```text
cmd/gateway/                 启动、依赖组装、关闭顺序
internal/core/               与基础设施无关的流程编排
internal/application/        鉴权后用例、路由策略、请求生命周期
internal/provider/           Provider 接口、能力声明、错误类型
internal/provider/llamacpp/  llama.cpp 适配器
internal/provider/ollama/    Ollama 适配器
internal/provider/openai/    OpenAI-compatible 适配器
internal/protocol/           协议规范化、SSE 与错误映射
internal/auth/               调用方凭据与模型 ACL
internal/observability/      计时、指标、请求事件
internal/audit/              有界异步日志队列与生命周期
internal/store/              持久化接口与 MariaDB 实现
internal/model/              Gateway 模型目录与本地文件信息分离
internal/settings/           配置加载、校验和默认值
internal/server/             HTTP 边界与管理 API
migrations/                  数据库迁移
docs/                        中文架构、协议和运维文档
```

## 3. 分阶段计划

### 阶段 0：基线核验与行为清单
状态：进行中。

任务：
- 固定 dev 基线，盘点对外 API、配置、数据库表、Provider 操作和日志事件。
- 记录 `/v1/models`、流式保活、错误返回、模型切换、限流和管理接口当前行为。
- 为关键路径建立最小回归测试，避免重构时意外改变已有行为。
- 准备可重复的性能样本、固定模型、并发参数与指标采集方法。

验收：有清晰的 API/配置兼容清单；能区分已确认缺陷、现有设计和未验证事项；没有在基线未清楚前大规模搬迁目录。

### 阶段 1：日志与请求生命周期可靠性（第一优先级）
状态：进行中。已完成流事件留存预算、审计队列生命周期保护、请求/响应上限、Gateway/上游状态分离，以及上游响应头/首字节等指标拆分；基线 CI 已通过，观察器热路径的压力测试仍待补齐。

重点文件：`internal/server/pipeline_hooks.go`、`internal/server/request_trace.go`、`internal/audit/writer.go`、`internal/llama/client.go`、`internal/server/request_analysis*.go`。

任务：
1. 统一请求开始、上游响应头、首字节、首个输出事件、首段正文、结束、取消和错误事件。
2. 分开记录客户端状态码与上游状态码；定义 TTFT、首字节和总耗时的起止口径。
3. 将正文存储、原始 SSE 存储、内容预览拆成独立配置，默认只保存必要元数据。
4. 对流事件设置每请求事件数、单条大小、总字节和保留期限限制。
5. 减少 Flush 前的同步日志解析、锁竞争和大块复制；非必要诊断细节进入有界异步消费者。
6. 明确队列满载策略；可丢弃的诊断事件必须计数，关键请求结果需有明确保障策略。
7. 修复队列关闭与并发入队竞态，按停止接收、等待在途请求、排空队列的顺序关闭。
8. 请求体严格限长并对超限返回 413；非流式响应也设置合理大小上限。
9. 补充中文配置说明、敏感字段脱敏和数据清理策略。

验收：日志异常、数据库暂时不可用、队列满载、客户端取消和进程关闭不引发 panic；关闭正文记录后流事件不残留 Prompt、响应、reasoning 或预览；日志不改变响应正文和流顺序；记录日志开关不同模式下的 TTFT、P95、输出速率、CPU、RSS、队列积压与丢弃量。

### 阶段 2：请求边界、鉴权和路由语义
状态：进行中。已完成模型 ACL 顺序与列表过滤、原始查询参数透传、可信代理 IP/CIDR 白名单、Gateway/上游状态码分离与数据库增量迁移；本组新增活动 Provider 健康检查需以本次 CI 验证。

任务：
- 将模型 ACL 检查提前到模型加载、远程请求和计费等副作用之前。
- 明确使用“单活动模型”还是“按请求 model 字段路由”，并统一 API 文档、管理台与测试。
- `/v1/models` 只返回当前调用方可访问的模型。
- 正确保留上游查询参数，定义 Header 转发策略。
- 明确超时、取消、客户端断连和流式响应提交后的错误行为。
- 只信任可信反向代理设置的客户端 IP。
- 将可信代理限制为显式 IP/CIDR；默认不读取来自非可信直连的转发头。

验收：无权限请求不会触发模型加载或 Provider 请求；模型列表与 ACL 一致；查询参数、取消和错误映射回归测试通过。

### 阶段 3：Provider 扩展边界
状态：进行中。已定义 Provider、Registry、Capabilities、Usage 和稳定错误类型；本地 HTTP Adapter 已接入唯一出网点，OpenAI-compatible JSON/SSE Adapter 已接入并通过基线 CI，后续继续做契约与多协议测试。

先建立精简稳定的核心概念：
- `Provider`：统一推理调用能力。
- `ModelDescriptor`：Gateway 模型 ID、上游模型 ID、Provider 引用和能力。
- `Capabilities`：协议、流式、工具调用、视觉等能力声明。
- `ProviderError`：鉴权、限流、模型不存在、超时、上游错误和协议错误分类。
- `LocalModelManager`：可选的本地加载、卸载和下载能力。
- `Usage`：Token 数量、来源和可信度；不能把估算值伪装成上游真实用量。

任务：先给现有 llama.cpp 和 Ollama 写契约测试，再定义接口，将两个本地后端适配到接口，拆分推理与本地管理能力。启动层负责组装依赖，Handler 中不堆叠厂商 if/else。

验收：本地后端通过同一套契约测试；添加新 Provider 不需要修改核心 Pipeline；没有为远程 Provider 强加本地管理空操作。

### 阶段 4：外部 Provider 与凭据管理
状态：进行中。

已完成：远程 Provider 静态配置、独立 API Key、模型 ID 覆盖、默认阻止私网及特殊用途 IP、禁止重定向、响应体上限与安全错误响应已接入；CI 基线通过。真实供应商端到端与故障注入测试仍待执行。

任务：
- 增加 Provider、远程模型映射和凭据引用的安全配置/存储。
- 首先接入一个 OpenAI-compatible 服务，验证 Base URL、模型 ID、API Key、超时、流式和错误映射。
- 再用 DeepSeek 等第二家兼容服务验证抽象，而不是复制整套转发代码。
- 不同协议（例如 Responses 或 Anthropic Messages）使用独立协议适配器，不假设所有 API 完全一致。
- 限制 Provider URL、HTTPS、重定向和内网访问，防止 SSRF 与密钥泄漏。
- 只在客户端尚未收到响应内容时按策略重试；流式开始后不自动切换以免重复输出。
- 成本统计保存 usage 来源与定价版本。

验收：本地与远程模型可统一路由；密钥不出现在日志和客户端响应；429、超时、鉴权失败等错误有稳定映射；流式中断不产生重复生成。

### 阶段 5：运行治理与可维护性
状态：进行中（早期阶段）。

任务：修复 Redis 限流计数与 TTL 的原子性；增加 Provider 并发限制、延迟/成功率/429/5xx/Token 指标、日志队列积压和丢弃指标；为管理台轮询增加分页、缓存和频率限制；加入 `go test ./...`、`go vet ./...`、构建与集成测试流水线；补齐中文运维文档。

验收：能够定位延迟、上游错误和日志丢失；管理台轮询不会明显拖慢推理；关键行为有自动化测试。

## 4. 提交策略

1. 一个功能阶段内可以多次编辑、局部 review 和测试，不逐个小逻辑提交。
2. 一个功能达到独立验收标准后，将代码、测试、中文注释和文档合并成一次提交。
3. 提交前检查格式、测试、静态检查、差异、敏感信息和配置兼容性。
4. 允许一个阶段拆成少量独立功能提交，但不把无关功能混在一起，也不为了减少提交把高风险改动强行捆绑。
5. 不强推或改写远端历史；每次写入前核对 dev 最新提交。
6. 示例提交信息：`fix: 修复流式日志热路径与请求指标`、`refactor: 抽离 Provider 接口并适配本地后端`、`feat: 增加 OpenAI 兼容模型接入`。

## 5. 进度更新规则

每完成一个可验证工作单元，更新本文进度，包含：当前阶段和任务比例、完成内容及文件、实际运行的测试命令和结果、新风险/阻塞、下一步任务，以及提交 SHA。比例代表任务清单估算，不代表测试通过率。不得将“代码已写”写成“测试通过”，不得将静态审查写成运行验证。

## 6. 当前进度

> 最近一次完整 CI 成功运行：提交 `08feb2e2be83bb81f4212649ebdacf1f3dfc74b5`，Actions 运行 `39889162077`（success）。该运行覆盖 Go 单测、`go vet`、Gateway 构建、管理台源码构建，以及重建 bundle 与已提交 `web/static/dist/app.js` 的精确字节对比。

| 阶段 | 状态 | 进度 | 说明 |
|---|---|---:|---|
| 阶段 0：基线与计划 | 进行中 | 95% | 分支基线与最近 CI 已核实；仍需补齐 API、配置、数据库兼容清单与可重复性能样本。 |
| 阶段 1：日志可靠性 | 进行中 | 95% | 已完成查询凭据/请求头脱敏、流事件留存预算、审计队列生命周期治理、请求/响应大小边界；同步观察器热路径与压力基线仍需继续评估。 |
| 阶段 2：请求边界与路由 | 进行中 | 95% | 已完成 ACL 顺序、模型列表过滤、RawQuery 透传、可信代理白名单、Redis Lua 原子限流、远程模型 ID 原样映射、请求级 Provider 固定/本地客户端隔离，以及取消与 Finish 终态保护（CI 已通过）。继续补 API/并发契约覆盖与性能基线。 |
| 阶段 3：Provider 抽象 | 进行中 | 90% | 已有 Provider 契约、Registry、Capabilities、本地 Adapter、可选 LocalModelManager 和统一推理出网点；还需通过更多 Provider/协议契约测试检验边界。 |
| 阶段 4：外部 Provider 与密钥 | 进行中 | 95% | YAML 环境变量安全展开、厂商自定义 Header/Query 脱敏、远程 Provider URL/SSRF 限制和流式/非流式错误正文脱敏已通过 CI。本组校验本地推理 BaseURL 的凭据边界，待 CI；真实供应商端到端和 DNS/网络故障注入仍待执行。 |
| 阶段 5：运行治理与 CI | 进行中 | 40% | CI 已加入源码构建产物与已提交 bundle 的精确字节比较，并已根据失败诊断重新生成过期 bundle；本提交删除临时同步脚本、恢复 `contents: read` 最小权限。压力测试和外部指标仍缺。 |

### 已核实的历史 CI 问题

- 旧版 `provider_mutation_test.go` 曾把 `Path/Body` 错放在 `core.State` 顶层；正确布局是嵌入 `core.Request`。当前 `provider_mutation_test.go` 已按真实字段布局修复，当前 `provider_runtime_test.go` 也与 `internal/core/types.go` 一致，不需要重复修改。
- 远程 SSE 测试曾出现字符串字面量语法错误，后续提交已修复；最近基线 CI 已通过。
- 远程流式非 2xx 已有回归测试：未提交响应时保持上游 HTTP 状态并返回安全 JSON；响应已提交时发出脱敏 SSE `event: error`。Responses API 错误事件已按协议使用顶层 `type/code/message/param/sequence_number`，并有序号观察器的分片/超大行边界测试。首轮 CI 的 `undefined: writeStreamError` 已在提交 `ad4b9a5` 修复，Actions 运行 `39780717182`（success）通过。真实 SDK 与真实供应商的端到端行为仍未验收。

### 下一步

1. 活动 Provider 健康检查已在提交 `d4fcda6` 的完整 CI 运行 `39777021005` 通过。
2. 已确认前端源码与部署 bundle 旧版本存在差异，当前 bundle 已重建。CI 严格比较保留，临时自动 push 已移除；本清理提交将验证当前 HEAD 的构建一致性。
3. 补齐 API、配置、数据库迁移与关键响应行为清单，并准备可重复的延迟/内存/并发基线。
4. 继续评估同步 SSE 解析/实时看板更新的热路径成本，对远程 Provider 做真实服务及故障注入测试。
5. 每完成一个有边界的功能组，再更新本文并按功能提交。

### 已验收功能组：请求级 Provider 固定

- `core.ModelChoice.ProviderID` 在 Resolve 阶段固定，后续 Mutator、LocalModelManager 和 Forwarder 必须沿用该身份。
- 内部 benchmark 可设置 `ServiceProviderID`，模型解析、加载、等待与转发使用同一个本地后端。
- 本地 Registry 按后端 ID 获取独立 HTTP 客户端；客户端按后端/URL 缓存，不依赖活动指针 `s.llama`。
- 自动加载通过上下文标记抑制全局活动模型采纳，防止旧请求完成后覆盖管理员刚选定的模型。
- 切换与自动采纳只在保存/应用配置的短阶段互斥，实际模型加载不持锁；新增路由固定、Mutator 选择、管理器选择、客户端缓存及 Provider 工厂回归测试。该功能组已由提交 `ef3e48dd90b82fc027863b23bd9432e47c8e855e` 的 Actions 运行 `39885016348` 全部通过。

### 已验收功能组：取消与 Finish 终态一致性

- `callState` 与 `runCancels` 共同记录 Finish 是否已开始及取消是否先被接受，确保两者具有明确先后顺序。
- Finish 已开始后管理端取消返回 409；取消先被接受时，即使上游同时返回成功也记录为 cancelled。
- `FinishRequest` 不覆盖数据库中已经写入的 cancelled 状态、状态码和取消原因。
- 内存中找不到活动请求且实时看板为空时，取消前冲刷审计队列，避免把异步 Finish 正在落库的记录误判为重启残留。
- 提交 `1c17e542677e2f448ec44d274d02f7849541b07d` 的 Actions 运行 `39886198853`（success）通过完整 CI。

### 已验收功能组：配置环境变量安全展开及必需字段校验

- `expandConfigEnv` 在 YAML 节点树上只展开字符串值，不再向原始 YAML 文本直接插入环境变量内容；配置键名保持固定。
- 未展开的必需凭据/连接字段在启动时返回字段路径级错误，不显示密钥值。
- 可选的登录项使用独立环境变量和原有默认逻辑；禁用远程 Provider 与非活动本地后端的未配置值不应阻止启动。
- 特殊字符标量、防止 YAML 注入、PBKDF2 哈希保真和缺失必需密钥失败关闭的回归测试，已由提交 `5e3be3a0d409b210479672e846336732d84c1afe` 的 Actions 运行 `39887506150`（success）通过。

### 已验收功能组：厂商自定义凭据 Header/Query 脱敏

- 提取共享的厂商密钥 Header 命名识别规则，同时用于请求头脱敏和审计 URL 查询参数脱敏。
- 覆盖 `X-DeepSeek-Key`、`X-Gemini-Key` 这类没有 `api` 字样的凭据头/参数。
- 保留 `X-Model-Key`、`model-key` 等普通字段，避免将全部 `*-key` 一概打码。
- 提交 `a6ca7efb59057adc79bfb931f1478354244632e2` 的 Actions 运行 `39888566042`（success）通过完整 CI。

### 已验收功能组：非流式 JSON 上游错误脱敏

- 非流式 `forwardJSON` 对非 2xx 使用与 SSE 一致的安全错误策略；原始成功响应仍保持透明转发。
- 按状态码映射稳定错误分类；保留上游 HTTP 状态、`Retry-After` 等白名单头和准确响应字节统计。
- 401、429、503 回归测试验证错误响应不包含上游调试正文和供应商 API Key；提交 `08feb2e2be83bb81f4212649ebdacf1f3dfc74b5` 的 Actions 运行 `39889162077`（success）通过完整 CI。

### 已验收功能组：本地推理 BaseURL 凭据边界

- 配置加载阶段验证非空本地 BaseURL 是绝对 HTTP(S) 地址，并拒绝 userinfo、query、fragment、无 host、非法 scheme、反斜杠和点段路径。
- 错误仅包含字段路径，不回显 URL/凭据信息；合法路径前缀仍然允许。
- 空的非活动本地后端保持可选，但非空的非活动地址同样要通过校验。
- 新增 Load 集成测试覆盖 URL 边界及合法配置；提交 `81fed06053a66e6f635889ce9909c1a7e54775ab` 的 Actions 运行 `39889800933`（success）通过完整 CI。

## 7. 中文文档与注释规范

- 架构文档、配置说明、运维步骤和重要设计决策使用中文。
- 公共接口和复杂逻辑添加必要中文注释，说明设计原因、边界条件、并发约束、错误语义和调用方责任。
- 不添加逐行重复代码含义的注释；优先使用清晰命名。
- 配置键、JSON 字段、HTTP 协议字段和 Go 标识符保持稳定，不为中文化破坏 API 兼容性。
- 对流式响应已提交后不能修改 HTTP 状态等不直观行为，在对应代码处说明原因。

## 8. 多 Agent 并行入口（新增任务，2026-10-23）

### 目标与约定

- 新增可选静态 YAML `agents` 配置，每条定义唯一 ID、独立 URL 前缀、Provider ID 和默认上游模型。
- 可同时启用 `/ollama/v1/*`、`/llama/v1/*`、`/deepseek/v1/*`、`/gpt/v1/*`，分别路由到 Ollama、llama.cpp、DeepSeek、OpenAI-compatible Provider；由配置决定，不在 Handler 中硬编码厂商。
- 现有 `/v1/*` 继续使用管理台活动 Provider，保持旧客户端兼容。
- Agent 入口固定一个配置模型；首阶段不维护动态模型池或故障转移策略。请求体里的 model 不覆盖入口固定模型，与现有全局入口的模型选择原则一致。
- Agent 入口复用现有 Gateway API Key 鉴权、限流、模型 ACL、请求审计、Pipeline、流式转发和统一 Provider 错误语义；供应商 API Key 仍只来自服务端配置。
- Provider 路由元数据由 HTTP 路由层写入私有 Context，外部 Header/body 无法修改选择结果；Agent 请求不会更新全局活动 Provider。

### 实施与验收

1. 启动时校验 Agent ID、path_prefix 唯一性、保留路径冲突、Provider 可用性和 default_model。
2. `/{prefix}/v1/models` 只返回入口绑定的模型，并执行调用方模型 ACL。
3. 测试前缀剥离、审计 URL 还原与 query 凭据脱敏、Agent 路由选择和全局活动 Provider 不变。
4. 后续评估 Agent 级模型别名/映射、独立访问策略、运行时热更新和受控故障转移。
5. 每个功能组提交后核对最新远程 HEAD 与对应 Actions；CI 未通过不能标记验收。
