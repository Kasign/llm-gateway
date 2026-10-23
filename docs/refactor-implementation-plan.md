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

> 最近一次已验收代码功能组：提交 `82bd2f5712167707cbfa528e2d9d31a10172cdd0`，GitHub Actions #108（运行 `39904722392`，success）。新增 Agent `/v1/models` 完整 HTTP 鉴权/ACL/RPM 集成；完整 Go 单测、race、性能基准与 Artifact、vet、Gateway 构建和前端 bundle 校验通过。

| 阶段 | 状态 | 进度 | 说明 |
|---|---|---:|---|
| 阶段 0：基线与计划 | 进行中 | 98% | 分支基线、API/配置/数据库契约清单已建立；CI 已产出第一组可重复基准，仍需在固定环境持续采样与比较。 |
| 阶段 1：日志可靠性 | 进行中 | 95% | 已完成查询凭据/请求头脱敏、流事件留存预算、审计队列生命周期治理、请求/响应大小边界；同步观察器热路径与压力基线仍需继续评估。 |
| 阶段 2：请求边界与路由 | 进行中 | 99% | 已完成 ACL 顺序、模型列表过滤、RawQuery 透传、可信代理白名单、原子限流、请求级 Provider 固定、取消终态保护、Agent 拒绝路径审计 URL 恢复；Actions #101 验证 JSON/SSE data-plane 隔离，#104 验证模型列表 ACL，#108 验证 Agent `/v1/models` 外层鉴权与真实 Redis RPM 429。持续性能趋势与线上压测仍待。 |
| 阶段 3：Provider 抽象 | 进行中 | 94% | 已有 Provider 契约、Registry、Capabilities、本地 Adapter、可选 LocalModelManager 和统一推理出网点；Agent data-plane 集成已验证两条远程 Provider 的 JSON/SSE 往返、各自密钥及模型固定。仍需更多协议/真实供应商契约验证。 |
| 阶段 4：外部 Provider 与密钥 | 进行中 | 98% | Anthropic Messages 协议边界由 Actions #85 验证；确定性 DNS NXDOMAIN/timeout、本机连接拒绝由 #92 验证；真实 ResponseHeaderTimeout 对 OpenAI/Anthropic 的 504 映射由提交 `3a9d560` 的 Actions #96 验证。真实供应商端到端及真实公网故障注入仍待执行。 |
| 阶段 5：运行治理与 CI | 进行中 | 67% | CI 的 race detector、请求取消、双 Agent 并发解析与双 Provider JSON/SSE data-plane 隔离、DNS/连接/响应头超时故障注入均通过；三项性能基准和 30 天 Artifact 已连续多次成功运行。仍需同环境持续趋势、压力/线上阈值与真实 Provider 故障注入。 |

### 已核实的历史 CI 问题

- 旧版 `provider_mutation_test.go` 曾把 `Path/Body` 错放在 `core.State` 顶层；正确布局是嵌入 `core.Request`。当前 `provider_mutation_test.go` 已按真实字段布局修复，当前 `provider_runtime_test.go` 也与 `internal/core/types.go` 一致，不需要重复修改。
- 远程 SSE 测试曾出现字符串字面量语法错误，后续提交已修复；最近基线 CI 已通过。
- 远程流式非 2xx 已有回归测试：未提交响应时保持上游 HTTP 状态并返回安全 JSON；响应已提交时发出脱敏 SSE `event: error`。Responses API 错误事件已按协议使用顶层 `type/code/message/param/sequence_number`，并有序号观察器的分片/超大行边界测试。首轮 CI 的 `undefined: writeStreamError` 已在提交 `ad4b9a5` 修复，Actions 运行 `39780717182`（success）通过。真实 SDK 与真实供应商的端到端行为仍未验收。

### 下一步

1. **已完成当前契约盘点**：新增 `docs/api-config-db-contracts.md`，覆盖公开/管理 API 分组、Provider/Agent 配置与生效边界、数据库表/迁移机制、错误和流式状态语义；README 已添加入口。该文档说明当前代码事实与风险，不替代 OpenAPI。
2. **并发契约与确定性网络故障已通过 CI**：取消/Finish 线性化和 400 个双 Agent 并发模型解析由 Actions #90 通过；DNS NXDOMAIN/timeout 与本机连接拒绝由 Actions #92 通过，且 race detector 通过。当前新增 Provider 转发、Agent 模型解析、请求分析三项 `-benchmem` 基线，由 CI artifact 记录 `ns/op`、`B/op`、`allocs/op`；首次测量待本提交 CI。
3. 对 OpenAI-compatible Provider 做真实供应商及 DNS、连接超时、429/5xx 故障注入，核验错误映射与凭据边界。
4. 只有在明确需求后再增加同一 Agent 多模型别名/映射、Agent 级访问策略和配置热更新；不允许任意客户端 model 值越权改变 Provider。
5. 受控重试/故障转移必须确认响应尚未提交；SSE 输出开始后禁止切换上游。
6. 每完成一个有边界的功能组，更新本文并按功能提交；提交后以当前 HEAD 对应 Actions 为验收依据。

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

## 8. 多 Agent 并行入口（已实现，CI 通过；后续增强待办）

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


### 本轮验收记录（2026-10-23）

- 功能提交：`66531b4ec192f61874e222645fc9a0395e1bbea6`，首次 CI #76 失败原因是 Agent 路由文件存在一个未使用的 `net/url` 导入。
- 修复提交：`657203485917406ff1f971209ff11a51fada7001` 删除未使用导入；GitHub Actions #77（运行 ID `39890738119`）通过完整 CI：Go 测试、`go vet`、Gateway 构建、管理台源码打包、bundle 一致性与关键字段检查。
- 路由回归覆盖配置的多 Provider 组合、非法/重复路径前缀、未配置 Provider、前缀剥离、审计 URL 查询凭据脱敏、路由模型选择及不改写全局活动 Provider。
- 配置属于进程启动配置；编辑 `agents` 或 Provider 配置后需按部署流程重启/重新部署网关。当前没有管理台热更新入口。
- 后续增强仍未包含：同一 Agent 内多模型别名映射、按 Agent 进行不同的 Key/权限策略、配置热更新、Provider 健康探测与响应提交前的受控重试/故障转移。故障转移不得在已经向客户端写入流式内容后执行。
- 流式上游错误已 review：上游非 2xx 且客户端响应未提交时返回保留 HTTP 状态的脱敏 JSON；状态已提交时追加安全 SSE `event: error`，不能修改已经发送的 HTTP 状态行。当前测试涵盖 JSON 401/429/503、SSE 状态提交前后与 Responses API 的序号字段。

### 多 Agent 入口拒绝请求的审计 URL 一致性复核（2026-10-23，已验收）

- 代码审查发现：Agent 路由通过 http.StripPrefix 将请求路径归一成 /v1/*，鉴权失败、限流拒绝及请求体超限分支此前直接记录剥离后的 URL。
- 由提交 `275a70e0849536978963cc4fa727e65ff9953044` 修复：拒绝请求日志构建抽成 `buildRejectedInferenceLog`，统一使用 `agentRequestPublicURL` 恢复外部路径。
- 回归测试 `TestRejectedInferenceLogPreservesAgentURLAndRedactsQuerySecrets` 验证 `/deepseek/v1/...` 前缀保留且查询 token 仍脱敏。
- GitHub Actions #79（运行 `39894282515`）完整通过：Go 单测、`go vet`、Gateway 构建、管理台源码构建和前端 bundle 检查。
- 下一步不是继续扩大本次修复范围，而是按上面的清单补充并发/契约测试和真实 Provider 故障注入。


### Anthropic Messages 协议透传修复（2026-10-23）

- 代码审查对照 README 的 `/v1/messages` 兼容声明，发现远程 Provider 的请求头白名单漏掉必需的 `anthropic-version`，并未针对 Anthropic Messages 使用其规定的 JSON/SSE 错误信封。
- 本功能组在同一 Provider 适配器中补充 `anthropic-version`、`anthropic-beta` 头透传；厂商凭据继续由 Provider 独立配置；未提交响应的错误统一生成 Anthropic JSON 错误信封，已提交流则使用 Anthropic SSE error 事件。
- 新增 Mock 上游测试：协议头与模型覆写、Gateway/Provider 凭据隔离、401 JSON 错误脱敏、流式 503 错误事件脱敏，以及客户端响应状态已提交时保持 200 的语义。
- 代码修改前基线为提交 `275a70e0849536978963cc4fa727e65ff9953044`，Actions #79（运行 `39894282515`）通过。实现提交 `37a3ae692b6f0560f520ea5b410a40b0fdc54d6c` 的 Actions #81（运行 `39894814380`）也完整通过：`go test ./...`、`go vet ./...`、Gateway 构建、管理台源码构建和前端 bundle 检查。
- 当前验收边界：Mock 测试覆盖上述协议头、凭据隔离、401 JSON 和已提交流的 503 错误；真实 Anthropic/Claude Code 端到端与 DNS、连接失败、超时等故障注入仍未验收。


### Anthropic Messages 兼容性补齐（2026-10-23，CI 已通过）

- 对照 [Claude Code gateway compatibility guide](https://code.claude.com/docs/en/llm-gateway-protocol) 与 [Claude API errors](https://platform.claude.com/docs/en/api/errors) 复核后，发现固定请求头白名单、统一错误正文脱敏和提前 warm-start 提交 200 会破坏部分 Claude Code 能力拒绝重试。
- 本功能组扩大 `anthropic-*` 请求头透传，补齐 `anthropic-ratelimit-unified-*`、`x-should-retry` 等响应头；在 Anthropic Messages 路径保留上游非 2xx 原始错误正文与状态，OpenAI-compatible 其他路径继续使用原来的脱敏规则。
- 对 `/v1/messages`（含 `count_tokens` 子路径）设置 32,000,000 字节请求上限；Gate 与 Provider 两层校验；拒绝响应和 Pipeline 错误使用 Anthropic JSON 错误信封，覆盖计费、权限、冲突、过大请求、速率限制、超时和过载类型。
- Anthropic Messages 流式请求不使用 Gateway warm-start 注释，避免先发送 SSE 注释而不可逆地提交 HTTP 200。
- 变更文件：`internal/provider/openai_compatible.go`、`internal/provider/openai_compatible_test.go`、`internal/server/pipeline_hooks.go`、`internal/server/pipeline_http.go`、`internal/server/keepalive_test.go`、本文档与 `docs/gateway-technical-guidance.md`。
- 兼容性与安全取舍：原始 Anthropic 上游错误正文可能包含供应商自行生成的诊断信息，因此只对管理员明确配置的 Anthropic Messages 上游保留；Provider URL/凭据边界继续由既有安全配置约束，非 Anthropic API 错误正文不改变。
- CI 结果：首次提交 `dac3f9f` 的 Actions #83 因 `safeBody` 旧变量名编译失败；修复提交 `9d6d31a` 的 Actions #84 揭示测试中的 `core.State` 嵌入字段误用；修复提交 `b3ec270421daad654c450b20c43782297f882f3f` 的 Actions #85（运行 `39896239166`）全量通过，包括 `go test ./...`、`go vet ./...`、Gateway 构建、管理台源码打包和部署 bundle 检查。过程中的失败及修正已保留在历史文档中。
- 验收边界：这证明当前自动化测试与构建通过，不代表真实 Anthropic/Claude Code E2E 已执行；DNS、连接超时及真实模型能力降级行为仍需真实供应商验证。


### CI #83 首轮失败与修复记录（2026-10-23）

- 提交 `dac3f9fb2d8d6a55bdefc745ec76bba42c9c80c5` 对应 Actions 运行 `39895993452` 在 `go test ./...` 阶段失败，日志确认唯一编译诊断为 `internal/provider/openai_compatible.go:348:15: undefined: safeBody`。
- 原因：Anthropic Messages 错误正文分支重构后实际写入变量已改成 `errorBody`，但 short-write 长度校验仍引用旧名称。
- 本修复将长度校验统一改为 `len(errorBody)`。需要重新运行完整 CI；修复提交对应 Actions 完成前不标记阶段 4 通过。


- CI #84（运行 `39896121255`）在修正 `safeBody` 编译错误后继续发现新增 Server 测试将嵌入的 `core.Request` 字段写在 `core.State` 顶层。按 `internal/core/types.go` 的真实类型定义改为 `Request: core.Request{...}`；该问题仅在测试编译阶段暴露，下一次 CI 需重新验证全量测试及后续构建步骤。


### 并发取消与 Finish 竞态回归（2026-10-23，CI 已通过）

- 新增 `TestRunCancelsConcurrentCancelAndFinishAreLinearized`，500 轮并发竞争 Cancel/MarkFinishing，验证线性化不变量：取消被接受则 Finish 必须观察到取消；Finish 先开始则迟到取消不得取消 context。
- GitHub Actions 新增 `go test -race ./internal/provider ./internal/server ./internal/core`，在既有常规单测之后验证 Provider、请求生命周期和 Pipeline 核心包的数据竞态。
- 基线为完整 CI #86（文档提交 `7a506f50b0cc90b3ace1afbb8e79f1f506a19394`，运行 `39896367757`，success）；后续 race detector 首次发现旧测试回调数据竞态，已修复并在 CI #88（运行 `39896961918`，success）通过复验。CI #90 也重新通过相同 race 检查。


- CI #87（运行 `39896716139`）的 race detector 揭示既有 `TestRepeatKeepaliveStopsOnWriteError` 测试回调与测试 goroutine 间对普通变量的无同步读写。提交 `c18c20f2f47885522cec40cf045d72966d3a1f78` 将回调结果改为缓冲 channel；其完整 CI #88（运行 `39896961918`）中 `go test ./...`、`go test -race ./internal/provider ./internal/server ./internal/core`、`go vet ./...`、Gateway 构建与前端 bundle 校验全部通过。


### 双 Agent 并发路由隔离测试（2026-10-23，CI 已通过）

- 新增 `TestConcurrentAgentRoutesKeepProviderAndModelIsolation`，对两组固定路由并发执行共 400 次模型解析。
- 每个请求的客户端 `model` 故意与路由模型不同；验收条件是每次结果都必须命中该 Agent 固定的 Provider ID、默认模型，且不能修改全局活动 Provider。
- 此测试只验证路由选择和并发隔离，不触发真实上游网络请求，也不代替供应商端到端测试。
- 基线：`c18c20f2f47885522cec40cf045d72966d3a1f78` 的 Actions #88（运行 `39896961918`）通过含 race detector 的完整流水线。本项由提交 `d41e34eb343439b536009a275969c0c49bfdc842` 的 Actions #90（运行 `39897322058`）验证通过；普通测试、race、vet、Gateway build 与前端构建/静态检查全部成功。


### 契约清单文档验收（2026-10-23）

- `docs/api-config-db-contracts.md` 与 README 链接在提交 `96f25fd29c2eef1e5b2ad061f96926d8877aeeb2` 中完成；对应 Actions #89（运行 `39897166275`）通过。
- 契约清单明确了 API 分组、Agent 路由固定语义、配置加载与热更新边界、当前六张必需表、缺少版本化迁移账本的风险，以及 Gateway/上游 HTTP 状态和流式提交边界。
- 此文档为现状索引，不替代 OpenAPI；下一步按优先级补性能基线与远程 Provider 网络故障测试。


### DNS 与连接层故障注入（2026-10-23，CI #92 已通过）

- 将私有 HTTP 客户端构造拆为默认入口与包内 Resolver 注入入口；生产环境仍使用 `net.DefaultResolver`，没有新增 YAML/HTTP 配置。
- 新增确定性 DNS 失败测试：NXDOMAIN 应映射为 502，Resolver timeout 应映射为 504；两者检查 Anthropic Messages 错误信封保持有效，并断言不泄露上游主机名或内部 DNS 诊断。
- 新增真实本机连接拒绝测试，核对 502 与安全错误信封；这些是本地故障注入，不是外部 OpenAI/DeepSeek/Anthropic 服务端到端验证。
- 提交 `b6415a39cd0d7ba3c9de3f30e87d4c288a9c176f` 对应 Actions #92（运行 `39897633772`）已通过完整流水线：`go test ./...`、`go test -race ./internal/provider ./internal/server ./internal/core`、`go vet ./...`、Gateway 构建和前端 bundle 校验均成功。真实 OpenAI/DeepSeek/Anthropic、DNS 出口策略及跨公网连接故障仍未验收。


### 可重复性能基线（CI #94 已通过，首轮结果已记录）

- 新增 `BenchmarkOpenAICompatibleProviderForwardJSON`：通过本机 loopback Mock 上游测远程 Provider 适配器/HTTP 转发开销，不将真实供应商网络或推理耗时混入 Gateway 基线。
- 新增 `BenchmarkResolveInferenceAgentRoute`：测固定 Agent Provider 与默认模型解析路径；新增 `BenchmarkBuildRequestAnalysis`：测请求时间线到分析 DTO 的转换/分配成本，不包含数据库写入。
- CI 以 `-benchmem -benchtime=1s` 运行三项基准，记录 `ns/op`、`B/op`、`allocs/op`，并上传为按 commit SHA 命名、保留 30 天的 Artifact。该步骤只记录，不设跨 Runner 硬阈值；比较需使用同一 Go 版本/相近 Runner。
- 首轮结果（Actions #94，Go 1.23.12，linux/amd64，AMD EPYC 7763；单 runner，`-benchtime=1s`）：
  - `BenchmarkOpenAICompatibleProviderForwardJSON`：90,352 ns/op，15,795 B/op，146 allocs/op。
  - `BenchmarkResolveInferenceAgentRoute`：342.0 ns/op，0 B/op，0 allocs/op。
  - `BenchmarkBuildRequestAnalysis`：1,372 ns/op，592 B/op，17 allocs/op。
- [下载性能基线 Artifact](https://github.com/Kasign/llm-gateway/actions/runs/39898164924/artifacts/12477692434)（30 天有效）。这些结果用于同一 CI Runner/Go 版本之间比较；Provider 值来自本机 loopback Mock HTTP 服务，不包含公网 RTT 或模型推理时间。首次样本不能证明性能提升，也不应直接作为生产 SLO。


### CI #93 工作流插入失败与修复（2026-10-23）

- 新增性能基线的提交 `6d81f565990061038836a7f8218c336967d8f987` 对应 Actions #93（运行 `39898004302`）在任何 Job 初始化前失败；GitHub 未创建运行 Job，因此没有执行 Go 测试、Benchmark 或构建步骤。
- 检查确认 `.github/workflows/go-ci.yml` 中新增的 Benchmark shell 命令插入位置破坏了 YAML 结构。当前修复直接从最后一份通过 CI 的工作流（`b6415a39cd0d7ba3c9de3f30e87d4c288a9c176f`，Actions #92）恢复，然后在唯一的静态检查步骤前重新插入 Benchmark 与 Artifact 步骤；原有测试、race、vet、构建及前端检查不做其他变更。
- 性能基线仍为待验收状态，必须由本修复提交触发的完整 CI 通过，并确认上传了 Benchmark Artifact 后才可标记完成。


### CI #94 性能基线验收（2026-10-23）

- 修复提交 `1339f5fb61537c08292aa44cec990c5d930401c9` 的 Actions #94（运行 `39898164924`）完整通过：`go test ./...`、`go test -race ./internal/provider ./internal/server ./internal/core`、三项 benchmark、artifact upload、`go vet ./...`、Gateway 构建、管理台源码构建与部署 bundle 校验。
- CI #93（运行 `39898004302`）曾在 job 初始化前失败；本次从上一份通过验收的工作流恢复并重新插入步骤后，#94 正常创建并执行全部步骤。未修改远端历史，也未把 #93 误计为代码测试失败。
- 后续应以同一 Go 版本/相近 Runner 进行多次采样后分析趋势；暂不设置单次 benchmark 阈值，以免 Runner 噪声造成误报。


### 上游响应头超时故障注入（2026-10-23，CI #96 已通过）

- 新增 `TestOpenAICompatibleProviderMapsResponseHeaderTimeoutTo504`，使用本地 Mock 服务接收请求但延迟响应头，缩短测试 Transport 的 `ResponseHeaderTimeout` 来真实触发 HTTP 客户端超时。
- 覆盖 OpenAI Chat Completions 和 Anthropic Messages 两条协议：内部错误映射必须是 `provider_timeout` / HTTP 504；Anthropic 路径必须返回 `timeout_error` 信封，且不泄露目标 URL/Transport 原始诊断。
- 提交 `3a9d56021c5eba0066287eccace7b3d7b499bbe8` 对应 Actions #96（运行 `39898522951`）通过完整流水线。该测试验证 Gateway HTTP 适配器与超时分类，不代表真实公网链路或供应商 SLA。


### Agent 路由真实 data-plane 集成测试（2026-10-23，CI #99 已通过）

- 新增 `TestAgentRoutesForwardToDistinctConfiguredProviders`，不只验证模型解析：通过真实 `agentRouteHandler`、固定 `core.Pipeline`、`inferenceForwarder` 与两个本地 HTTP Mock Provider 完成请求往返。
- 对 `/deepseek/v1/chat/completions` 与 `/openai/v1/chat/completions` 分别断言上游只收到标准 `/v1/chat/completions` 路径、各自原始 query、Agent 默认模型与对应 Provider 凭据。
- 客户端故意提交不匹配的 `model`，并带 Gateway `Authorization` 与 `X-API-Key`；断言出站请求不会泄露调用方凭据、不会跨 Agent 使用另一条路由的模型/上游，同时全局活动 Provider 保持 `llamacpp`。
- 该集成测试仅绕过外层 API Key/Redis 限流与审计观察器（这些已有独立测试），实际执行路由归一、模型固定、Pipeline Mutator/Guard、Provider 选择、HTTP 转发及响应回传。提交 `4ef97c9d9f748db1fd4aa23312ab728a5a1b49f4` 对应 Actions #99（运行 `39899193731`）通过完整 CI：常规单测、race detector、性能基准与 Artifact、`go vet`、Gateway 构建和前端 bundle 校验均成功。


### Agent data-plane 集成测试 CI #98 首轮失败（2026-10-23）

- Actions #98（运行 `39899077307`）在 `go test ./...` 阶段失败。日志显示 Provider Registry 依据既有安全配置规则拒绝测试中使用的 `api_key_header: x-upstream-key`，允许的自定义凭据头是 `Authorization`、`api-key` 或 `x-api-key`。
- 这属于新增 Mock 测试夹具不符合项目配置契约，不是路由实现失败。提交 `4ef97c9` 将服务端上游密钥改为受支持的 `api-key` 头，同时仍发送假的调用方 `Authorization` 与 `X-API-Key`；Actions #99 已通过，验证上游收到 Agent 专用 Provider 密钥，但不会把调用方凭据透传。


### Agent 流式 data-plane 契约（2026-10-23，CI #101 已通过）

- 在双 Agent、双 Mock Provider data-plane 集成测试中，为 DeepSeek 与 OpenAI 两条路由各执行非流式 JSON 和流式 SSE 请求。
- 非流式继续检查响应 ID、model、内容和服务端默认模型；流式检查响应的 text/event-stream Content-Type、对应 Provider 的增量内容和 [DONE] 终止事件。
- 两种模式均检查路径前缀剥离、query 透传、Agent 专用 API Key 与调用方凭据隔离，并校验出站 stream 标志与入站一致。
- 提交 `4e7e134a7a19fbe3dc1b2efb05f4383c0bcff978` 对应 Actions #101（运行 `39899727297`）已通过全量流程：普通 Go 单测、`go test -race ./internal/provider ./internal/server ./internal/core`、三项性能基准、Artifact 上传、`go vet ./...`、Gateway 构建、管理台源码构建及前端 bundle 校验全部成功。
- Artifact：[`llm-gateway-performance-baseline-4e7e134a7a19fbe3dc1b2efb05f4383c0bcff978`](https://github.com/Kasign/llm-gateway/actions/runs/39899727297/artifacts/12478586036)，30 天有效。该测试仍使用本机 Mock 上游，不等同真实公网供应商 E2E。


### Agent /v1/models 模型发现与 ACL 契约（2026-10-23，CI #104 已通过）

- 将 Agent 分支的模型列表组装逻辑提取为纯函数 `agentModelListBody`，供 `GET /<agent-prefix>/v1/models` 复用，避免模型列表行为分散在 HTTP 收尾逻辑内。
- 新增单元测试覆盖不受限 Key、按公开模型 ID 放行、按上游模型 ID 放行、其他模型被 ACL 拒绝；断言列表只暴露该 Agent 固定的目标模型与 Provider 标识，不把另一个 Agent 或客户端传入的任意模型列出来。
- 测试覆盖模型列表契约与 ACL 过滤，不替代完整外层鉴权、Redis 限流或审计流程集成。提交 `65d8709ff2fdad3a1c9e59c2b9d300a173f4cac5` 的 Actions #104（运行 `39900465325`）已通过全量流程：Go 单测、race detector、性能基准与 Artifact、`go vet`、Gateway 构建、管理台源码构建和部署 bundle 校验均成功。


### Agent /v1/models helper 首轮代码复核（2026-10-23）

- 在 CI #103 运行期间的二次 diff review 发现，抽取 Agent 模型列表生成函数后，日志仍引用原函数内的 `data` 变量，存在编译错误风险。
- 修复为 `agentModelListBody` 同时返回响应 JSON 和已 ACL 过滤后的模型数量；handler 使用返回数量写审计日志，单元测试也核对返回数量与 JSON 中的 data 数组一致。
- CI #103（运行 `39900242374`）确因 `internal/server/server.go:418:107: undefined: data` 编译失败；修复提交 `65d8709` 将 helper 返回的模型数用于日志，CI #104 已通过全部步骤。


### Anthropic Messages 成功流式与 count_tokens 契约（2026-10-23，CI #106 已通过）

- 新增成功 SSE 透传测试，覆盖 message_start、content_block_start/delta/stop、message_delta 与 message_stop，验证响应 Content-Type、上游 Request-Id、Anthropic-Version、query、服务端模型覆盖和 API Key 边界。
- 新增 /v1/messages/count_tokens 正向透传测试，检查路径与 query 不丢失、模型由 Provider 配置覆盖、Anthropic-Version 转发、Provider API Key 隔离，以及响应 JSON 原样返回。
- 这些是本机 Mock HTTP 协议测试；没有真实 Anthropic/Claude Code 端到端凭据或服务可用，因此不据此声称外部供应商完成认证。
- 提交 `4e5d69766e9a16a668e2398b918def97cb64d3f2` 对应 Actions #106（运行 `39900997516`）已通过全量流程：`go test ./...`、`go test -race ./internal/provider ./internal/server ./internal/core`、三项性能基准、性能 Artifact 上传、`go vet ./...`、Gateway 构建、管理台源码构建与部署 bundle 校验全部成功。


### Agent /v1/models HTTP 鉴权、ACL 与 RPM 集成（2026-10-23，CI #108 已通过）

- 新增专用 HTTP Handler 集成测试，使用 test-only `database/sql/driver` 模拟 API Key 仓库与异步审计数据库，不新增第三方测试依赖。
- 通过实际 `Server.Handler()`、Agent 前缀路由、`v1Models`、`auth.Repo.GetByRaw`、`Limiter.Allow` 与审计队列，覆盖无 Key/无效 Key 返回 401、不受限 Key 只能看到 Agent 的固定模型、限制到其他模型的 Key 返回空列表。
- 另有 Redis 可选集成用例：读取 CI 已配置的 `REDIS_TEST_ADDR`，同一 Key 的首个请求通过，超过 RPM 后的后续请求必须返回 429；本地未配置 Redis 时该单项跳过，其他测试仍可运行。
- test SQL driver 只模拟 API Key 查询行与成功写入，不验证 MariaDB 方言或 schema；数据库迁移已有独立测试覆盖。RPM 用例在 CI 中使用真实 Redis 服务。提交 `82bd2f5712167707cbfa528e2d9d31a10172cdd0` 对应 Actions #108（运行 `39904722392`）已通过全量流程：Go 单测、race、三项性能基准及 Artifact、`go vet`、Gateway 构建、管理台源码构建和部署 bundle 校验均成功。
- Artifact：[`llm-gateway-performance-baseline-82bd2f5712167707cbfa528e2d9d31a10172cdd0`](https://github.com/Kasign/llm-gateway/actions/runs/39904722392/artifacts/12480765287)，30 天有效。
