# LLM Gateway 重构实施计划与进度

> 目标分支：`dev`
> 更新日期：2026-10-27。PR #5/#7 已合入；当前以 `dev` 为唯一日常集成分支。真实供应商 E2E、公网反向代理和生产部署验证仍依赖用户环境。
> 最近完整 CI 基线：`5946e529ec213f41561452417af6dbd797282d2c`（Actions [#246](https://github.com/Kasign/llm-gateway/actions/runs/40446504395)，五个并行 Job 全部通过，约 1 分 58 秒）。

## 当前状态快照（2026-10-27）

- 最近完整 CI Head `5946e52`（Actions #246）验证 RequestLog 与 RequestDetail 摘要/耗时字段契约，以及配置/API、Go 测试/race/vet、负载故障注入、Docker 构建均通过。
- CI 已拆为四个并行 Job，并启用 npm 缓存、Buildx GitHub Actions 层缓存，以及同一 ref 的过期运行取消。Actions #241 约 2 分 05 秒、#242 约 2 分 03 秒；相比串行 Actions #240 约 4 分 10 秒，墙钟时间接近减半。
- 已合入的最近功能包括：本地 GGUF 清单与运行时状态隔离、部署 Provider FIFO 参数透传、Token 预算按实际出站 dispatch 结算、高风险 OpenAPI/Go 字段对账与请求取消状态契约。
- 当前没有开放 PR。保持非强制更新、不改写历史；按完整功能组集中提交，避免小改动反复触发完整 CI。
- CI 使用 Mock、Redis 与 MariaDB；真实供应商及生产部署验收单独登记，不以 CI 成功代替。

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
状态：进行中（YAML/环境变量/数据库 Web Provider 配置与管理台已经在 PR #7 实现；当前继续治理 Provider 热更新期间的并发许可生命周期、配置回滚与并发预算生效顺序）。

已完成：远程 Provider 静态配置、独立 API Key、模型 ID 覆盖、默认阻止私网及特殊用途 IP、禁止重定向、响应体上限、安全错误响应、Web 多 Provider 管理、`.env` 多 Provider 预置与每 Provider 整体请求时限均已实现。真实供应商端到端与公网故障注入仍待执行；本轮按用户要求不等待 CI，后续统一测试。

任务：
- 增加 Provider、远程模型映射和凭据引用的安全配置/存储。
- Web 管理台新增多 Provider 管理页：列表/新增/编辑/启用/停用/删除、测试连接、查看已配置模型与模型 ID 映射；配置变更必须经过后端校验，不允许前端绕过 URL/SSRF 和协议限制。
- 允许从 `.env` 预置一个或多个远程 Provider，适用于部署时注入 Base URL、模型名、超时和密钥；密钥使用环境变量引用或仅写入，不通过读取 API 回传明文。
- 在实现前明确环境预置配置与 Web/数据库配置的合并规则：如何识别配置来源、同 ID 冲突时的优先级、是否允许 Web 修改环境托管项、删除/停用的实际语义、何时生效及重启后如何恢复。兼容现有“数据库中已保存的活动上游设置优先于文件默认项”的行为，避免部署升级后意外切换正在使用的上游。
- 对 Web 创建的 Provider，定义密钥的持久化保护、掩码展示、修改时留空表示保留、显式清除、审计与导出脱敏策略。
- 首先接入一个 OpenAI-compatible 服务，验证 Base URL、模型 ID、API Key、超时、流式和错误映射。
- 再用 DeepSeek 等第二家兼容服务验证抽象，而不是复制整套转发代码。
- 不同协议（例如 Responses 或 Anthropic Messages）使用独立协议适配器，不假设所有 API 完全一致。
- 限制 Provider URL、HTTPS、重定向和内网访问，防止 SSRF 与密钥泄漏。
- 只在客户端尚未收到响应内容时按策略重试；流式开始后不自动切换以免重复输出。
- 成本统计保存 usage 来源与定价版本。

### 本功能组配置来源、优先级与生效边界（实施方案）

为同时支持部署预置和 Web 动态管理，配置来源必须可识别，不做静默的字段级覆盖：

1. **兼容现有 YAML 配置。** 保留 `providers.openai_compatible[]` 与现有 `${ENV_NAME}` 展开逻辑；原有部署无需迁移。该来源标记为“部署配置”，由 YAML/部署环境维护。
2. **支持直接用 `.env` 预置完整 Provider。** 新增统一前缀 `LLM_GATEWAY_PROVIDER_<ID>_`，至少支持 `ENABLED`、`BASE_URL`、`DEFAULT_MODEL`、`API_KEY`，并支持 `API_KEY_HEADER`、`REQUEST_TIMEOUT_SECONDS`、`MAX_CONCURRENT_REQUESTS`、`MAX_RESPONSE_BYTES`、`ALLOW_INSECURE_HTTP`、`ALLOW_PRIVATE_NETWORK` 及模型映射配置。`REQUEST_TIMEOUT_SECONDS=0` 不增加 Provider 级总时限，`1–86400` 秒限制完整 JSON/SSE 请求。环境变量 ID 段转为小写后作为 Provider ID；变量值必须走与 YAML 相同的规范化、URL/SSRF、安全和范围校验。真实 Key 仅放在部署环境，不提交到 Git。
3. **Provider ID 跨来源冲突一律失败关闭。** YAML、前缀环境变量和数据库中 Web 创建的 Provider 合并成可用目录；同一 ID 在多个来源出现时给出不含密钥值的启动/保存错误，不按“谁优先”覆盖整份配置。这样不会因增加环境变量而意外替换正在使用的 Provider。原有 `gateway_settings.llm_upstream` 仍只代表全局活动 Provider/模型选择，不覆盖某个 Provider 自身的地址、模型或凭据。
4. **部署管理项只读，Web 管理项动态生效。** YAML/环境变量来源在 Web 列表中显示来源并禁止直接编辑、删除；变更需修改部署配置并重启。Web 新建项保存在专用 Provider 配置存储，更新时先完整校验并持久化，再以不可见中间态的方式切换运行时 Registry；请求已拿到旧 Provider 实例时允许其完成，不中断在途 SSE。关闭或删除当前活动 Provider 前必须先切换到其他 Provider。
5. **Web 密钥不明文持久化或回显。** Web 管理创建的 Provider Key 需要使用 `LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY` 提供的独立 32 字节密钥做 AEAD 加密后再落库；密钥缺失/格式不合法时，不允许保存带 Key 的 Web Provider。API 只返回 `api_key_configured` 与掩码状态，永远不返回明文。编辑表单留空表示保留原 Key；单独的显式清除动作需要二次确认。日志、错误、测试产物和导出均不得包含 Key。
6. **连通性测试不调用计费推理。** 使用短超时请求测试 Provider 的模型列表端点，禁止重定向，复用正式 Provider 的 URL/SSRF 与 Header 校验；区分 DNS/TLS/连接失败、HTTP 鉴权错误和不支持模型列表端点等情况，向 UI 展示脱敏结果，不保存上游原始错误正文。
7. **模型映射和全局活动选择分离。** Provider 编辑支持默认模型与公开模型 ID 到上游模型 ID 的映射；`/v1/models` 只发布当前活动 Provider 对外可用的模型。添加 Provider 不自动改变全局活动 Provider；现存数据库里的活动选择继续优先生效。
8. **按功能层验收。** 先完善环境变量解析、来源标记、冲突检查和存储加密；再实现后端 CRUD/测试连接/原子运行时更新；最后实现 UI 与部署 bundle。覆盖部署预置、Web 新建、同 ID 冲突、Key 保留/轮换/清除、运行时切换、活动 Provider 删除保护、重启恢复及多 Provider 隔离。

验收：本地与远程模型可统一路由；密钥不出现在日志和客户端响应；429、超时、鉴权失败等错误有稳定映射；流式中断不产生重复生成。

### 阶段 4.5：配置来源治理与部署样例一致性（新增计划）

状态：实施中（第一批已加入模板对账与环境变量契约门禁）。当前不仅有 `.env` 与 `config.yaml`，还存在 `providers.env`、`config.example.yaml`、数据库中的 Web Provider 配置，以及 Go 代码中的默认值。分层并非天然不合理，但字段职责与默认值必须能被追踪，且不能让同一个配置项在多个位置以不透明的优先级覆盖。

目标：保持部署简单、兼容现有用户配置，同时建立可审计的配置来源规则。**不以“所有配置必须塞进一个文件”为目标，也不在未完成兼容盘点前删除旧配置入口。**

实施顺序：
1. **先做配置清单**：逐项记录配置名、用途、来源（环境变量/YAML/数据库/代码默认值）、是否敏感、是否可热更新、是否需重启、生效优先级与对应代码位置。
2. **明确唯一职责边界**：
   - `.env`：部署环境变量、凭据与密钥，以及需要由部署环境提供的宿主机/网络参数；不再把 YAML 已有的非敏感默认项无说明地重复维护。
   - `config.yaml`：结构化的服务行为与非敏感默认值，例如请求超时、输出上限、日志/保留策略、下载参数、代理信任和 Agent 路由。敏感值只通过明确的环境变量引用，不在 YAML 保存真实密钥。
   - `providers.env`：仅保留需要通过环境预置任意 Provider ID 的高级入口；不能把主 `.env` 整份注入 Gateway，也不能和 YAML/其他来源静默覆盖同 ID Provider。
   - Web/数据库：用于运行时动态管理的 Provider 和活动上游选择，明确哪些字段运行时可改、哪些由部署层托管只读；避免把活动 Provider 选择和 Provider 静态凭据配置混为一谈。
3. **收敛重复与冲突**：审查 Compose `environment` 白名单、环境变量解析和 YAML 展开逻辑；移除重复的非敏感 Provider 默认值入口，明确环境变量覆盖的例外项。对跨来源同 ID 冲突继续失败关闭，而不是引入“最后一个值覆盖前一个值”的隐式行为。
4. **消除样例漂移**：以 `config.example.yaml` 作为可复制模板，逐项比较当前跟踪的 `config.yaml` 与示例；补充配置一致性检查，防止默认值、配置字段和部署文档再次分叉。确认仓库中真实部署配置的跟踪策略，避免把机器专属配置误当成规范模板。
5. **提供向后兼容迁移**：对旧环境变量和旧 YAML 字段给出兼容期、弃用提示和迁移说明。先更新部署说明与示例，再切换代码读取路径；不得让升级静默改变慢速本地模型的超时、日志留存、Provider 活动选择或密钥加密行为。
6. **统一回归**：覆盖纯 YAML、纯环境 Provider、YAML+环境冲突、Web Provider、慢模型超时、敏感配置展开、Compose 环境白名单和示例一致性。集成验证集中安排，不为每一个小文件单独触发测试循环。

本阶段的完成标准：
- 每项配置只有清晰的主维护入口，跨来源优先级与热更新/重启语义有文档。
- 示例、Compose、Go 配置解析和部署文档一致；样例差异能够被自动检查发现。
- 老部署不因迁移丢失配置；配置冲突失败时错误信息不泄露密钥。
- 文档写明必须重启的配置和可以通过 Web 动态调整的配置。

### 阶段 5：运行治理与可维护性
状态：进行中（早期阶段）。

任务：修复 Redis 限流计数与 TTL 的原子性；增加 Provider 并发限制、延迟/成功率/429/5xx/Token 指标；审计队列深度、成功/失败、按原因丢弃与排队时延的只读 API 已实现并由 Actions #162 全量通过；DNS 故障审计语义由 #163、响应已提交后的 SSE 中断语义由 #164 验收通过。继续覆盖陈旧 Keep-Alive 连接恢复、日志保留策略和管理台轮询负载；TLS 握手中断已有 Actions #166 验收。保留策略默认不主动删除历史数据，只有显式启用后才按批次清理；保持 `go test ./...`、`go test -race`、`go vet ./...`、构建与集成测试为每个功能组的验收门槛。

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

## 6. 当前进度（2026-10-27）

> 最近完整 CI：Head `064f5c9b20ae8fa633b762606ccd53fdeca39b22`，Actions #242 全部通过。#241/#242 并行运行约 2 分钟；真实供应商和生产环境验收仍单独进行。

| 阶段 | 状态 | 进度 | 说明 |
|---|---|---:|---|
| 阶段 0：基线与计划 | 持续维护 | 98% | API、配置、数据迁移与 OpenAPI 目录已建立；性能数据需要在可比 Runner 上持续采样。 |
| 阶段 1：日志可靠性 | 自动化门禁通过 | 99% | 审计队列、MariaDB 积压、Gateway 负载、DNS/TCP/TLS/SSE 故障、竞态及状态一致性有完整 CI 覆盖；公网代理与真实供应商仍需部署验证。 |
| 阶段 2：请求边界与路由 | 自动化门禁通过 | 99% | ACL、请求取消终态、Provider 固定、Agent 路由、RawQuery 透传和限流有回归；持续线上性能趋势仍待。 |
| 阶段 3：Provider 抽象 | 持续演进 | 94% | Registry、Provider 契约、本地 Adapter、OpenAI-compatible JSON/SSE 与统一出网点已实现；真实多厂商协议行为未验收。 |
| 阶段 4：外部 Provider 与密钥 | 自动化门禁通过 | 98% | YAML/环境/Web 多来源、Key 加密/脱敏、模型映射、并发、RPM、时限与热更新已有实现；供应商真实 E2E 仍待。 |
| 阶段 4.5：配置来源治理 | 代码与 CI 门禁基本完成 | 96% | 配置矩阵、环境变量展开、ID 冲突拒绝、根目录 Compose 与配置契约门禁已完成；旧环境迁移与实际 1Panel 配置仍需用户环境验收。 |
| 阶段 5：运行治理与 CI | 持续收尾 | 96% | 每日 Token 预算、FIFO 队列、截止时刻保护、部署参数透传和 CI 并行缓存已实现；继续补 Provider 热更新预算回归和 API 字段契约。 |

### 下一步

1. **收紧 Provider 热更新回归**：补齐队列容量缩小/扩大、停用 Provider 编辑、重新启用和非法容量等状态组合；检查持久化失败/Registry 替换失败时共享并发与队列预算的回滚。
2. **分阶段补齐 OpenAPI Go 契约**：已检查 Provider mutation/view、模型列表 Go 字段、请求列表 Provider 筛选和取消状态码；继续按风险覆盖其他管理 API，整体契约状态保持 `partial`。
3. **完成配置来源与部署边界 Review**：保留旧 `.env` 兼容回退，不在没有配置备份/比对时改变用户实际部署数据。
4. **集中整体 Review**：复核取消/Finish、SSE 已提交后的错误状态、慢速本地模型超时传播、密钥脱敏、MariaDB 迁移和源码/部署 Bundle 同步。
5. **真实环境验收单独登记**：真实供应商 usage/计费、公网 OpenResty、1Panel 网络及慢模型延迟需要用户凭据和部署环境；CI 不能代替这些验收。
6. 继续按逻辑功能批次提交并更新中文文档；新 CI 并行化后目前约 2 分钟，之后以实际 Actions 为准。
### 已核实的历史 CI 问题

- 旧版 `provider_mutation_test.go` 曾把 `Path/Body` 错放在 `core.State` 顶层；正确布局是嵌入 `core.Request`。当前 `provider_mutation_test.go` 已按真实字段布局修复，当前 `provider_runtime_test.go` 也与 `internal/core/types.go` 一致，不需要重复修改。
- 远程 SSE 测试曾出现字符串字面量语法错误，后续提交已修复；最近基线 CI 已通过。
- 远程流式非 2xx 已有回归测试：未提交响应时保持上游 HTTP 状态并返回安全 JSON；响应已提交时发出脱敏 SSE `event: error`。Responses API 错误事件已按协议使用顶层 `type/code/message/param/sequence_number`，并有序号观察器的分片/超大行边界测试。首轮 CI 的 `undefined: writeStreamError` 已在提交 `ad4b9a5` 修复，Actions 运行 `39780717182`（success）通过。真实 SDK 与真实供应商的端到端行为仍未验收。

### 下一步

1. **完成配置来源治理的最后一轮 Review**：目前 `.env`、`config.yaml`、`providers.env`、Web/数据库配置和 Go 默认值已有职责矩阵与 CI 静态门禁。代码确认 YAML/环境重复 Provider ID 在加载校验阶段失败，Web Provider 与部署配置 ID 冲突时拒绝覆盖。下一步核对默认值、旧 `.env` 兼容回退和敏感字段错误输出；在用户部署环境验证之前不删除 `config.yaml` 或强行迁移配置目录。
2. **请求日志保留功能**：`GET /api/admin/retention/status` 已加入 `dev`，返回启用状态、最近执行状态、耗时、删除行数和失败计数；不访问数据库、不回显原始错误，指标在进程重启后清零。完整 Actions #225 已通过，真实部署后的轮询/告警接入仍需由运维环境配置。
3. **收口 OpenAPI 字段级契约**：当前 `docs/openapi.yaml` 有 63 个字面路径模板和 72 个 HTTP operation；Agent 路由前缀展开后，CI 对 67 个运行时路由路径做双向对账。operationId、summary、responses、security 元数据门禁已通过；后续在碰到具体 Handler 时继续补请求/响应 Schema，不把静态路径检查说成完整语义验证。
4. **推进尚未实现的运行治理功能**：Provider 每日 Token 预算正在实现，待本批 CI 验证；请求排队策略仍待设计，先明确公平性、队列上限、等待超时、取消移除、Redis 失效语义和请求已经提交响应后的行为。
5. **需要用户环境的项目单独验收**：真实供应商 E2E、真实公网 OpenResty 路由和生产故障恢复需要用户自己的供应商密钥与部署环境。自动重试/故障转移、Agent 级独立访问策略和 Agent 配置热更新仍是显式延后项，先做风险设计，不为填进度表而贸然实现。
6. 每完成一个完整功能组，同步中文设计说明与问题台账，按功能合并提交；以对应最新 HEAD 的 GitHub Actions 作为自动化验收依据，不重复手动触发小范围测试。

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


### 显式开启的真实 Provider E2E（用例已提供，尚未连接真实供应商验收）

- 新增 `TestOpenAICompatibleProviderRealEndpointE2E`，默认 `t.Skip`，仅当 `LLM_GATEWAY_PROVIDER_E2E=1` 时发起真实网络请求；要求本地配置 HTTPS BaseURL、API Key 与 Model。API Key 不进入代码、日志或测试输出。
- 请求走实际 `OpenAICompatibleProvider.Forward` 和安全 HTTP Transport，发送极短、无用户数据的 Chat Completions 请求，并检查 HTTP 200 与可解析的 choices。默认请求体最多请求 8 个输出 token，90 秒上下文截止。
- 环境变量：`LLM_GATEWAY_E2E_BASE_URL`（例如供应商的 HTTPS `/v1` Base URL）、`LLM_GATEWAY_E2E_API_KEY`、`LLM_GATEWAY_E2E_MODEL`；可选 `LLM_GATEWAY_E2E_API_KEY_HEADER`（留空则采用默认 Authorization 方式）。
- OpenAI-compatible 用例在 Actions #111 已验证默认跳过路径和编译，Anthropic Messages 用例在 Actions #116 已验证默认跳过路径和编译；两者均未配置真实供应商 Key，不会发起真实网络请求。真实供应商响应、计费、权限、限流和端到端 SLA 仍需用户控制的环境单独验收。


### 真实 Provider E2E 用例首轮 CI 复核（2026-10-23）

- Actions #110（运行 `39905269876`）在普通 Go 测试阶段发现新增测试使用了不存在的 `OpenAICompatibleConfig.DefaultModel` 字段；模型 ID 实际由每次 `InferenceCall.ModelID` 提供。
- 已移除错误字段。该失败只说明 E2E 测试夹具无法编译，没有发起真实供应商请求；修复提交会重新跑全量 CI。真实端点验收仍要求用户在本地显式设置 opt-in 环境变量和自己的 API Key。


### Responses API 成功 JSON/SSE 数据面契约（2026-10-23，CI #114 已通过）

- 新增 `TestOpenAICompatibleProviderForwardsResponsesJSONAndSSE`，通过真实 Provider HTTP adapter 与本地 Mock 上游分别走非流式 JSON、流式 SSE 两条成功路径。
- 验证 `/v1/responses` 路径、原始 query、`OpenAI-Beta` 透传、`Accept` 和 stream 模式匹配、客户端 model 被服务端选择的模型覆盖、Provider Authorization 凭据覆盖调用方凭据、调用方 `X-API-Key` 不透传。
- JSON 校验 response ID/model/status/output；SSE 校验事件序列和内容逐字节透明转发、`Request-Id` 与 `text/event-stream` 响应头。
- 上游使用 loopback Mock，不代表真实 OpenAI Responses/Codex 端点 E2E。提交 `d8bcafee44ccbb7a4a5bd459bf363563ccbb1e1a` 对应 Actions #114（运行 `39906018427`）已通过完整 CI：常规测试、race detector、性能基准与 Artifact、`go vet`、Gateway 构建、管理台源码构建和前端 bundle 校验均成功。


### Responses API 测试夹具二次 Review（2026-10-23）

- 在 Actions #112 运行期间检查新增 Go 测试源码，发现第一版 SSE 字符串转义容易被生成工具重复转义，非流式请求体也使用占位字符串替换。
- 已改为 `json.Marshal` 生成输入，使用 `json.RawMessage` 解析上游请求和响应，并通过缓冲通道接收 Mock 上游观测值，避免共享捕获变量带来的 race 风险。修复提交会重跑完整 CI；Responses 生产实现未修改。


### Responses 测试字符串转义最终检查（2026-10-23）

- 再次逐字符检查生成的 Go 源文件后，发现 Responses fixture 字符串中的转义序列仍被生成层加倍；先前提交 42fddee 的 CI 用于识别该情况，不计作验收。
- 当前测试源码已把 Go 字符串中的转义恢复为单层，继续使用 JSON Marshal 和通道收集 Mock 观测值。以修复后的新提交全量 CI 为准。


### 显式开启的 Anthropic Messages E2E（用例编译与默认跳过路径由 CI #116 验收；真实端点未运行）

- 新增 `TestAnthropicMessagesRealEndpointE2E`，默认跳过，只在 `LLM_GATEWAY_ANTHROPIC_E2E=1` 时发起网络请求；需要 HTTPS BaseURL、Anthropic API Key 和 model 环境变量。
- 请求使用真实 Provider 安全 Transport，固定 `x-api-key` 与 `Anthropic-Version: 2023-06-01`，发送一条无用户数据的短 Messages 请求，最多请求 12 个输出 token，并在 90 秒后截止。
- 测试只检查 HTTP 200、assistant message envelope 和至少一个 content block；不打印密钥、上游原始错误或响应内容。默认 CI 不访问外网，不需要 GitHub Secret。
- 提交 `594ef12048e0a9dee52bb6c1ae4d780f1cbfaa3a` 的 Actions #116（运行 `39906558311`）通过完整 CI，证明用例可编译并在默认环境跳过；未配置 Anthropic API Key，因此没有调用真实 Claude/Anthropic 端点。真实供应商验收仍待用户在安全环境运行。


### 显式开启的 OpenAI Responses E2E（用例已提供，真实端点待运行）

- 新增 `TestOpenAIResponsesRealEndpointE2E`，单独用 `LLM_GATEWAY_RESPONSES_E2E=1` 开启，复用 `LLM_GATEWAY_E2E_BASE_URL`、`LLM_GATEWAY_E2E_API_KEY`、`LLM_GATEWAY_E2E_MODEL` 和可选 API Key Header。默认不联网。
- 请求真实 `/v1/responses` 路径，发送一次短输入并把输出限制为 8 token、90 秒截止，验证 HTTP 200 及 Responses 的 `object/id/status/output` 封套。密钥和响应正文不写日志。
- CI 仅验证编译和默认跳过路径；真实 OpenAI/Codex 端点尚未在当前环境调用。


### 多 Agent Responses API data-plane 集成（2026-10-23，CI #120 已通过）

- 新增 internal/server/agent_responses_test.go 中的 TestAgentRoutesForwardResponsesJSONAndSSEToConfiguredProviders，通过真实 agentRouteHandler、core.Pipeline、inferenceForwarder 与两个本机 HTTP Mock Provider 验证完整请求往返。
- 同时覆盖 /deepseek/v1/responses 和 /openai/v1/responses 的 JSON、SSE 成功路径；检查 Agent 前缀去除后转发为 /v1/responses、原始 query 保留、请求体中的客户端 model 被 Agent 固定模型覆盖、stream 标志一致，以及各自上游凭据互不串用。
- 测试带入调用方 Authorization 与 X-API-Key，确认二者不会作为凭据泄露给远程 Provider；同时确认 Agent 请求不会修改全局活动 Provider。
- 首次提交 bc07bf2e67c1f2df162232550c67ce8ed74db31a 对应 CI #119 因新增测试文件中存在未使用的 context 导入而在编译阶段失败，没有执行测试。删除该导入后，修复提交 8f578d9a7b5ec3e426e830533116cd4cf874c518 对应 Actions #120（运行 39918244515）全量通过：go test ./...、go test -race ./internal/provider ./internal/server ./internal/core、三项性能基线及 Artifact 上传、go vet ./...、Gateway 构建、管理台源码构建和部署 bundle 校验。
- Actions #120 完整记录与性能 Artifact：https://github.com/Kasign/llm-gateway/actions/runs/39918244515。该集成使用本机 Mock Provider，不代表真实 OpenAI、DeepSeek、Codex 或其他供应商端点已经通过 E2E；真实端点用例仍为 opt-in，需在用户控制、配置实际凭据的环境中单独执行。


### 流式观察器热路径性能基线（2026-10-23，CI #123 已通过）

- 在 internal/server/performance_benchmark_test.go 新增 BenchmarkRequestTraceRecordStreamChunk，测量 SSE 分片解析与事件元数据生成，不连数据库、不保存正文预览或原始 SSE。
- 新增 BenchmarkInferenceObserverOnChunkParallel，以多个独立请求并行执行长流达到事件保留上限后的稳态 onChunk 路径，覆盖 token 解析、响应字节计数、请求锁和 Live Session 标量更新；不发真实模型请求、不连接数据库，也不开启 payload 留存。
- .github/workflows/go-ci.yml 的性能基准名单已纳入这两项，CI 每次推送都会保存可下载的 -benchmem 输出。Actions #123（运行 39919200540，提交 27e165860efe579101d0594f1e34d5c283a9cce7）全量 CI 通过，包含 Go 单测、race detector、五项性能基准、go vet、Gateway build 和前端检查。
- 首轮样本（Linux amd64，GitHub Actions Runner：AMD EPYC 7763，Go 1.23.12）：SSE parser/event metadata 为 2,888 ns/op、1,104 B/op、26 allocs/op；并行 observer onChunk steady-state 为 5,589 ns/op、1,760 B/op、39 allocs/op。该数值仅作为首轮基线，不可直接理解为性能提升或退化，也不应与不同 Runner 的结果直接比较。
- [Actions #123 与基准 Artifact](https://github.com/Kasign/llm-gateway/actions/runs/39919200540)（Artifact ID 12486836685，30 天有效）。Actions #122 首轮因 benchmark fixture 把 Stream 放在 core.Request 而编译失败；修复后 #123 全绿。此工作单元只新增测量，不改生产热路径。


### 审计队列与实时会话并发压力测试（2026-10-23，CI #128 已通过）

- 新增 `internal/audit/writer_stress_test.go`。测试将单 Worker 的首个任务阻塞，12 个生产协程共尝试入队 6,000 个普通任务；在队列大小 64、普通任务上限 56 时，验证队列保持有界、普通任务丢弃计数准确、8 个预留槽位仍可接收关键任务，队列完全满载后再拒绝一个关键任务，并确认关闭时 65 个已接受任务均完成执行。
- 新增 `internal/live/session_stress_test.go`。12 个协程共执行 9,000 次 StreamEvent 与标量更新，同时持续读取详情快照和列表摘要；断言并发快照始终不突破事件和正文边界，最终保留事件数不超过 2,000。快照采样限制为每毫秒一次，避免测试自身制造无界的复制/分配压力。
- 正文留存边界用例向 Session 提交超出上限的数据，验证请求正文与响应正文各自最多保留 8 MiB。
- `.github/workflows/go-ci.yml` 的 race detector 命令扩展为 `./internal/provider ./internal/server ./internal/core ./internal/audit ./internal/live`。Actions #128（运行 `39922580011`，提交 `79e295f8cbf33226453bb8ad1545d740a2ec4273`）全量通过：Go 单测、五项性能基线、Artifact 上传、覆盖上述五个包的 race detector、`go vet`、Gateway 构建和前端 bundle 校验。
- [Actions #128 与性能 Artifact](https://github.com/Kasign/llm-gateway/actions/runs/39922580011)，Artifact ID `12487543536`，30 天有效。该压力测试验证内存内数据结构和队列治理，不等同真实流量或 MariaDB 写入压力测试；后续仍需真实请求负载和持续性能趋势验证。


### MariaDB 审计写入与队列积压压力测试（2026-10-23，Actions #134 已通过）

- 新增 `internal/audit/writer_mariadb_test.go`，通过真实 `go-sql-driver/mysql` 连接 MariaDB，默认在未配置 `MYSQL_TEST_DSN` 时跳过；连接后会检查所选数据库名必须以 `_test` 结尾，避免误对生产库制造压力。
- 测试暂停单 Worker 后，由 10 个生产协程入队 500 条请求的创建、实际上游模型字段更新和最终结果写入，共 1,500 个生命周期 SQL 操作；随后继续推送 400 个普通分析更新和 3,000 个关键请求字节更新，验证 2,048 容量队列在积压时保持有界、普通日志不占用关键预留容量、关键任务可使用预留槽，并精确核对拒绝任务计数。
- 释放 Worker 后，测试直接读取 MariaDB 校验 500 条请求记录全部完成、已接收的 292 条分析字段更新均落库，以及关键更新仍按 FIFO 顺序执行到最后一个已接收值。
- `.github/workflows/go-ci.yml` 配置 `mariadb:11.4` 服务和专用 `llm_gateway_test` DSN，并增加独立的 `MariaDB 审计积压集成测试` 步骤。PR #1 的 Actions #134（运行 `39925049261`）全量成功：Go 单测、该 MariaDB 集成测试、race detector、五项性能基线与 Artifact 上传、`go vet`、Gateway build 和前端 bundle 检查全部通过。Artifact ID `12489171605`，链接：[Actions #134](https://github.com/Kasign/llm-gateway/actions/runs/39925049261)，有效期至 2026-11-22。
- 本测试验收的是单 Worker 真实数据库写入、队列积压策略和关停排空；并不等同生产请求流量下的吞吐/P95、线上故障恢复或真实供应商 E2E。


### Gateway 突发与稳态 HTTP 请求、Redis 鉴权及 MariaDB 审计负载（2026-10-23，Actions #141 已通过）

- 新增 internal/server/request_load_mariadb_test.go：使用实际 Server.Handler、MariaDB API Key/请求日志、Redis 限流器、OpenAI-compatible Provider HTTP Adapter 与本机 httptest Mock 上游。所有请求执行 HTTP 往返并经过 Gateway 鉴权、路由、Provider 转发、异步审计路径；上游 Mock 固定 2ms 延迟，不模拟模型推理或 token 生成。
- 负载测试默认跳过，必须显式设置 GATEWAY_LOAD_TEST=1；CI 仅在命名独立步骤启用，普通单测与 race detector 不重复执行该 5 秒负载。数据库名必须以 _test 结尾，测试创建唯一 API Key，结束时按 Key 名清理此次请求记录。
- 第一阶段是突发样本：16 个并发 worker，共 192 次请求。Actions #141 观测窗口 180ms、吞吐 1,064.7 req/s、P50 10.18ms、P95 25.15ms、P99 72.38ms、失败数 0；192 次请求都到达 Mock 上游。
- 第二阶段按每 5ms 投递一次，总计 1,000 次请求，目标 200 req/s、持续约 5 秒，最多 16 个请求同时在途。实测 4.999s、200.0 req/s、P50 3.19ms、P95 7.18ms、P99 187.45ms、失败数 0。P99 明显高于 P95，是当前短样本里需要继续复测的长尾，不据此设置性能阈值。
- 两个阶段合计 1,192 次请求。测试 Flush 审计队列后直接查询 MariaDB，确认 persisted=1192/1192、successful=1192、model_matched=1192，排空时长约 1ms。未发生 HTTP 错误，实际 Mock 上游收到 1,192 次请求。
- Go 内存观测：两阶段累计 TotalAlloc 增量 69,651,152 bytes，测试结束的 HeapAlloc 为 2,379,600 bytes。/usr/bin/time -v 报告：User CPU 2.91s、System CPU 1.16s、命令墙钟 6.59s、峰值 RSS 180,496 KiB（约 176.3 MiB），退出状态 0。这是 go test 进程包含测试初始化与清理的资源样本，不等于独立 Gateway 进程的线上 RSS。
- GitHub Actions #141（运行 39928285619，提交 8a56315b1ed673f70eb8aeaefbdf7456ad565be6）全量通过：Go 单测、MariaDB 积压集成、该负载步骤、race detector、五项性能基线与 Artifact、go vet、Gateway 构建和前端 bundle 校验。
- [Actions #141](https://github.com/Kasign/llm-gateway/actions/runs/39928285619)。负载报告 Artifact ID 12489709983（gateway-request-load-012da56adcb0250ff70296513d6b1e708a4a998e），性能基线 Artifact ID 12490985570；均保留 30 天，预计于 2026-11-22 过期。
- 第二轮重复样本：Actions #142（运行 39928783557）也全量通过。突发阶段 192 次、16 并发、220ms、871.4 req/s，P50 17.02ms、P95 29.75ms、P99 37.12ms、失败 0；稳态 1,000 次目标 200 req/s、4.999s、达到 200.1 req/s，P50 3.50ms、P95 4.27ms、P99 5.23ms、失败 0。共 1,192/1,192 条审计记录成功且模型字段正确，排空约 1ms。
- 第二轮资源记录：TotalAlloc 增量 69,394,544 bytes、结束 HeapAlloc 2,468,200 bytes；User CPU 3.21s、System CPU 1.43s、命令墙钟 6.73s、峰值 RSS 178,564 KiB（约 174.4 MiB）。负载 Artifact ID 12490352431，性能基线 Artifact ID 12490492858，预计均于 2026-11-22 过期。[Actions #142](https://github.com/Kasign/llm-gateway/actions/runs/39928783557)。
- 两轮稳态 achieved throughput 都约为 200 req/s；但第一轮 P99 为 187.45ms，第二轮为 5.23ms，P95 分别为 7.18ms 和 4.27ms。这说明 5 秒、1,000 请求样本对尾延迟仍敏感，不应用单次结果宣称性能改善，也暂不据此设置阈值。
- 此样本覆盖 HTTP 处理和持久化路径，但只运行 5 秒、响应由本地 Mock 生成，不能等价为真实 Provider E2E、长时间稳态容量、流式 decode 速率或生产线上压测。下一步应在相同 Runner 重复样本，再增加更长持续时长和可控延迟/错误注入。

### Gateway 60 秒稳态 HTTP 请求与 MariaDB 审计验收（2026-10-23，Actions #145 已通过）

- 基于已合并的 5 秒负载测试，将持续阶段延长至 12,000 个请求，按每 5ms 安排一次，目标 200 req/s、持续约 60 秒；前置 16 并发突发阶段保留 192 个请求。临时测试 API Key RPM 提高到 30,000，避免测试自身限流。测试仍显式要求 GATEWAY_LOAD_TEST=1，仅由 CI 中独立步骤运行。
- 请求经由真实 Server.Handler、MariaDB API Key/请求日志、Redis 限流器、OpenAI-compatible Provider HTTP Adapter 和本机 Mock 上游，Mock 固定 2ms 延迟，不包含真实模型推理。测试要求每一个客户端 HTTP 请求成功、每一个请求均抵达 Mock 上游，且所有请求日志状态及模型字段均正确。
- Actions #145 的突发阶段：192 次、16 并发、420ms、456.8 req/s，P50 28.63ms、P95 61.33ms、P99 81.97ms、失败 0。突发速率受 Runner 调度及前几条连接初始化影响，不应与更长阶段直接比较。
- Actions #145 的 60 秒稳态阶段：12,000 次请求、目标速率 200 req/s、耗时 59.999s、实际 200.0 req/s、P50 4.45ms、P95 5.76ms、P99 8.23ms、失败 0。连同突发阶段，Mock 上游总共收到 12,192 次请求。
- MariaDB 排空校验为 12,192/12,192 条记录持久化，12,192 条状态为 success，12,192 条 model_actual 正确；审计队列 Flush/排空耗时 4.359s，未观察到日志丢失。由于排空阶段单独计时，此耗时表示请求负载停止后仍需排空的尾部工作量，而不是整整 60 秒持续阶段的总 DB 工作耗时。
- 资源数据：Go HeapAlloc 结束值 6,151,520 bytes，TotalAlloc 增量 688,022,944 bytes；/usr/bin/time -v 的 User CPU 17.89s、System CPU 16.36s、命令墙钟 1:07.41、峰值 RSS 184,772 KiB（约 180.4 MiB），进程退出状态 0。以上 RSS 是 go test 进程，不等于独立线上 Gateway 的 RSS。
- GitHub Actions #145（运行 39929748367，提交 410f415bc2754fa643b8e4906b03aaefb9f06f83）全量通过：Go 单测、MariaDB 审计积压测试、60 秒 Gateway 负载、race detector、五项性能基线与 Artifact、go vet、Gateway build、前端源码和 bundle 一致性校验全部成功。
- [Actions #145](https://github.com/Kasign/llm-gateway/actions/runs/39929748367)。60 秒负载报告 Artifact ID 12491177225（gateway-request-load-ca52af8f034fc89fcabde7ceb3ddba8b8d748ea7），性能基线 Artifact ID 12490664029；保留 30 天，预计于 2026-11-22 过期。
- Actions #146 的第二轮 60 秒复测也全量通过：突发阶段 192 次、16 并发、664ms、289.3 req/s，P50 21.75ms、P95 388.91ms、P99 406.93ms、失败 0；稳态 12,000 次、59.999s、200.0 req/s，P50 3.96ms、P95 7.21ms、P99 42.62ms、失败 0。MariaDB 再次核对 12,192/12,192 条成功记录且模型字段正确，排空耗时约 2ms。第二轮资源：TotalAlloc 增量 699,113,120 bytes，结束 HeapAlloc 2,593,080 bytes，User CPU 12.29s、System CPU 4.65s、命令墙钟 1:03.48、峰值 RSS 184,768 KiB；负载 Artifact ID 12491686320，性能基线 Artifact ID 12491462016。[Actions #146](https://github.com/Kasign/llm-gateway/actions/runs/39930263577)。
- 两轮 60 秒稳态均达到 200 req/s、失败为 0，MariaDB 均完整持久化 12,192 条记录；但 P99 从 8.23ms 到 42.62ms，排空时长也从 4.359s 到约 2ms。说明在当前 Runner/Mock 配置下存在尾延迟与审计积压的样本间波动，暂不据此宣称性能改善或设置硬性阈值。该合成压力测试仍不等同真实供应商 E2E、流式 decode 负载或生产线上压测。


### Gateway 上游可控延迟与 429/503 故障注入（2026-10-23，Actions #149 已通过）

- 在已验收的真实 Gateway Handler、Redis 鉴权/限流、OpenAI-compatible Provider 和 MariaDB 审计 fixture 上增加独立故障模式 `GATEWAY_FAULT_TEST=1`。该模式绕过 60 秒常规稳态段，因此可单独重复验证错误语义。
- 120 个请求、最多 16 并发，四种场景各 30 次：正常 200、上游固定延迟 75ms、上游 429 并带 `Retry-After: 1`、上游 503。实测 120/120 请求符合预期，状态数为 200/429/503 = 60/30/30，故障阶段总耗时 259ms。
- 75ms 延迟场景的 P50/P95 为 80.69/99.92ms，正常对照的 P50 为 9.02ms。429 状态保留且 `Retry-After: 1` 头透传；429/503 错误体里的测试专用敏感诊断短语没有泄露到客户端。
- Flush 审计队列后直接查询 MariaDB，确认 120 条审计记录全部存在，客户端与上游状态分别以 60 条 200、30 条 429、30 条 503 正确记录；故障阶段队列排空时间为 384ms。
- PR #4 对应 Actions #149（运行 `39932315169`）中，该步骤、60 秒稳态负载、全量 Go 测试、race detector、性能基线、`go vet`、Gateway build 和前端 bundle 校验均通过。[Actions #149](https://github.com/Kasign/llm-gateway/actions/runs/39932315169)。故障注入 Artifact ID `12492046864`，负载 Artifact ID `12491363872`，性能基线 Artifact ID `12491613288`；三者计划保留 30 天，预计于 2026-11-22 过期。
- 本测试只覆盖 OpenAI-compatible JSON 路径的本机可控故障，不替代公网 DNS/连接异常、真实供应商限流/过载语义或生产级故障恢复演练。


### Gateway TCP 连接拒绝与恢复（Actions #155 已通过，2026-10-23）

- 在同一个 Gateway Handler、Redis API Key 鉴权/限流、OpenAI-compatible Provider 和 MariaDB 审计实例中开启 `GATEWAY_TRANSPORT_FAILURE_TEST=1`。先关闭本机 Mock 上游 TCP listener，对 Gateway 发起 30 个并发请求，预期每个都返回安全的 HTTP 502，且由于没有收到上游 HTTP 响应，`upstream_http_status` 保持为空。
- 随后在同一 host:port 重新启动同一 Mock Handler，发起 30 个恢复请求，预期每个均返回 200，且 `upstream_http_status=200`。测试直接 Flush 异步审计队列后查询 MariaDB，核对总共 60 条记录及两种状态的精确数量。
- Actions #155（运行 ID `39938948242`）中的 `Gateway TCP 连接拒绝与恢复验证` 已通过。日志明确记录 `failed_requests=30`、Gateway 502 且 `upstream_http_status=absent`；同一 host:port 恢复后 `recovered_requests=30` 且状态为 200。MariaDB 断言通过：总记录数 60，拒绝阶段 30 条上游状态为空，恢复阶段 30 条 `http_status=200` 且 `upstream_http_status=200`。日志 Artifact ID `12494887037`。
- 这是本机 loopback TCP 连接拒绝/恢复测试，不等同公网 DNS 故障、真实供应商网络恢复、进程重启或生产容量验收。


### 上游 HTTP 状态与 TCP 传输失败区分（2026-10-23，Actions #155 已通过）

- 实时核对 PR #5：目标分支 `dev`，工作分支 `test/gateway-transport-failure-recovery`，修复前 Head 为 `ef3627726ec364c338f6b1aa5377a10ca016c409`，PR 保持 open、未合并。PR base / `dev` HEAD 为 `950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。
- Actions #154（运行 ID `39935257603`）在“Gateway 上游延迟与 429/503 故障注入”失败。日志中的 MariaDB 聚合结果：120 条记录，Gateway 的 200/429/503 为 60/30/30，但 upstream 的 429/503 为 0/0。该次运行的单测、MariaDB 审计积压和 60 秒 Gateway 负载步骤通过；后续连接恢复、race、性能基线、go vet、构建与前端检查因前置步骤失败而跳过。
- 根因：`core.Pipeline.Run` 曾用 `UpstreamHeadersMS` 是否为空推断是否收到 HTTP 响应，但该计时字段不是 Provider 状态契约，部分 JSON 路径不填该字段。远程 JSON/SSE 与本地 Provider 没有在所有已收响应路径显式填充 `Result.UpstreamStatus`；本地非流式超限路径还曾把 Gateway 502 放进状态返回值。
- 已给远程 Provider 所有拿到 `http.Response` 的 JSON/SSE 返回分支、本地 Provider 的 JSON/SSE 结果显式设置 `UpstreamStatus`；连接拒绝等 `http.Client.Do` 失败场景不填上游状态。Pipeline 不再依据状态码或 `UpstreamHeadersMS` 推断上游状态。本地响应体超限时保留已收到的真实上游状态，Gateway 仍由错误映射返回 502。
- 新增 Provider 契约测试：JSON/SSE 对真实收到的 Mock HTTP 429、503、200 均记录真实状态；TCP 连接拒绝保持上游状态 0 并映射 Gateway 502。集成层继续保留既有严格 MariaDB 断言：429/503 阶段 120 条日志中 Gateway 与上游状态必须分别为 60/30/30；TCP 拒绝 30 条记录为 Gateway 502 且上游状态为空，同一端口恢复后 30 条记录为 Gateway 200 / upstream 200。
- 验收边界：以上为本机 loopback Mock 上游与 CI MariaDB 集成测试，不是 OpenAI、DeepSeek、Anthropic 或其他真实供应商 E2E，也不代表生产公网故障恢复或生产容量验收。Actions #155 全量通过后，本功能组的 CI 验收已完成；后续小节明确记录未覆盖的场景。


### Actions #155 完整验收结果（2026-10-23）

- [Actions #155](https://github.com/Kasign/llm-gateway/actions/runs/39938948242) 对提交 `be8f759f45de4da60dc5819a9db79c1ccb91d44d` 的完整结论为 `success`。单测、MariaDB 审计积压、60 秒负载、429/503 故障注入、TCP 拒绝与恢复、race detector、性能基线、`go vet ./...`、Gateway build、前端安装/源码 bundle 构建与部署 bundle 一致性校验均通过。
- 60 秒负载：突发 192 次请求、16 并发、365ms、526.4 req/s、P50/P95/P99 为 27.12/47.52/52.69ms、失败 0。稳态 12,000 次请求、目标和实际速率 200 req/s、持续 60s、P50/P95/P99 为 4.39/6.10/16.87ms、失败 0。突发与稳态总上游请求 12,192 次；MariaDB 持久化 12,192/12,192 条，全部 success、全部 model 字段匹配，审计排空 5.991s。Go test 进程最大 RSS 为 184,880 KiB；该值是 CI 测试进程数据，不代表独立生产 Gateway 的 RSS。
- 故障注入：120 次请求、16 并发，全部符合客户端状态/正文契约；Gateway 状态为 200/429/503 = 60/30/30。MariaDB 聚合断言同时通过，包含上游 429 为 30 条、上游 503 为 30 条；Retry-After 透传、错误正文脱敏、120 条审计记录及队列排空均通过。正常请求 P50 为 11.04ms，受控 75ms 延迟的 P50/P95 为 82.19/100.88ms，排空耗时 353ms。故障 Artifact ID `12495206308`。
- TCP 拒绝/恢复：30 条请求因连接拒绝返回 Gateway 502，所有上游状态为空；同一端口恢复后 30 条请求返回 Gateway 200 且上游状态 200。MariaDB 中 60/60 条均已写入且数量精确。Artifact ID `12494887037`。
- 性能基线（仅记录，不设阈值）：`BenchmarkOpenAICompatibleProviderForwardJSON` 101,399 ns/op、15,793 B/op、145 allocs/op；`BenchmarkResolveInferenceAgentRoute` 316.3 ns/op、0 B/op、0 allocs/op；`BenchmarkBuildRequestAnalysis` 1,428 ns/op、592 B/op、17 allocs/op；`BenchmarkRequestTraceRecordStreamChunk` 3,005 ns/op、1,104 B/op、26 allocs/op；`BenchmarkInferenceObserverOnChunkParallel` 4,680 ns/op、1,760 B/op、39 allocs/op。性能 Artifact ID `12493859918`。
- 请求负载 Artifact ID `12493734955`。这些 artifacts 按工作流配置保留 30 天，预计 2026-11-22 过期。

### 仍未覆盖的边界

- 上游契约测试及故障注入都基于 loopback Mock，不是真实供应商 E2E；没有验证真实供应商在流量整形、配额策略、边缘代理和各自错误信封下的具体行为。
- TCP 拒绝/恢复测试在同一进程内关闭并重绑 Mock listener；未覆盖公网 DNS 传播、跨网络路由恢复、连接池长时间陈旧、TLS 握手中断、已收到响应头后上游断流、进程重启后的恢复。
- 现有 DNS 错误测试验证安全错误映射/脱敏；新增长的 `UpstreamStatus=0` 断言直接覆盖 JSON/SSE TCP 拒绝路径，没有单独对 DNS 测试重复断言 upstream 字段为空。DNS/连接错误的运行路径都是未取得 HTTP response 时返回 Provider 传输错误，但 DNS 状态持久化仍应作为后续可补充的专项测试。
- 60 秒压力覆盖真实 Gateway Handler、Redis、MariaDB 审计与 Mock Provider，但不等价生产推理吞吐、长周期内存稳定性或生产容量认证；性能基准不设门槛，跨 Runner 的尾延迟不可直接横向比较。


### 远程 Provider 并发上限验收（2026-10-23，Actions #158 已通过）

- [Actions #158](https://github.com/Kasign/llm-gateway/actions/runs/39943828510) 对 PR #7 最终功能提交 `dd54cf576ba4a80e7c5d1f2dd0bb3be95a776b4b` 的结论为 `success`，26 个步骤成功、0 个失败。单测、MariaDB 审计积压、60 秒 Gateway 负载、429/503 故障注入、TCP 拒绝与恢复、race detector、性能基线、`go vet`、Gateway 构建及前端 bundle 检查全部通过。
- 并发配置：`providers.openai_compatible[].max_concurrent_requests`，默认值 64，上限 64，0 采用默认值。达到上限时 fail-fast 返回 `503/provider_concurrency_limit`，不会向上游发送额外请求；由于没有发生上游 HTTP 交互，`UpstreamStatus=0`。如果流式响应已提交，则追加安全 SSE error；Responses API 错误事件序号从 0 开始。
- 测试覆盖 JSON 请求达到上限、释放许可后恢复、配置范围拒绝，以及流式响应已提交时保持 Gateway 已提交状态并输出安全错误；全部包含在完整 CI 中。
- 60 秒负载：突发 192 次请求、16 并发、446ms、430.2 req/s、P50/P95/P99 为 29.09/64.42/112.34ms、失败 0；稳态 12,000 次请求、200.0 req/s、持续 60.001s、P50/P95/P99 为 4.46/5.90/9.82ms、失败 0。突发与稳态共 12,192 次上游请求，MariaDB 持久化 12,192/12,192 条成功记录且模型字段全匹配，审计队列排空 7.338s。/usr/bin/time 报告 Go test 最大 RSS 184,968 KiB；这是测试进程数据，不代表独立生产 Gateway RSS。负载 Artifact ID `12497481860`。
- 故障注入：120 次、16 并发、失败 0，Gateway 状态 200/429/503 = 60/30/30；MariaDB 上游 429/503 统计均为 30 条。正常请求 P50 为 11.74ms，受控 75ms 延迟场景 P50/P95 为 85.76/104.05ms，审计排空 393ms。Artifact ID `12496729363`。
- TCP 拒绝与恢复：30 条连接拒绝均返回 Gateway 502 且上游状态为空；同一端口恢复后 30 条返回 Gateway 200 / upstream 200；60 条审计全部写入。Artifact ID `12497103104`。
- 性能基线（不设阈值）：ForwardJSON 90,663 ns/op、15,798 B/op、146 allocs/op；路由解析 330.9 ns/op、0 B/op、0 allocs/op；请求分析 1,354 ns/op、592 B/op、17 allocs/op；流式追踪 3,006 ns/op、1,104 B/op、26 allocs/op；并发观察器 4,361 ns/op、1,760 B/op、39 allocs/op。Artifact ID `12497447132`。

### 首轮并发 PR 基线依赖错误与修复记录

- PR #6 的 Actions #157 在编译阶段失败，日志指出新代码引用了 `core.Result.UpstreamStatus`，但该分支从尚未合并 PR #5 的旧 `dev` 分支切出，类型字段不存在。
- 没有删除测试断言或移除上游状态语义；关闭了未合并 PR #6，并从 PR #5 已通过的 Head `ac0549109703ab4257ab43c0da8ef0cd5b5b1232` 建立堆叠分支 `feat/provider-concurrency-limits-stacked`。PR #7 的 Actions #158 已基于含有该类型契约的完整代码全量通过。
- PR #5 与 PR #7 当前仍均为 Open/未合并。PR #7 在 PR #5 合并前不应独立合并；PR #5 合并后需将 PR #7 base 调整为最新 `dev` 并复核差异及再次执行 CI。
- 真实供应商 E2E 未执行。此轮所有 HTTP 故障注入和负载均使用本机 loopback Mock 上游及 CI Redis/MariaDB，不代表公网故障恢复或生产容量验收。


### 2026-10-26 执行记录：审计指标与多 Provider Web 配置规划

- 审计指标 `internal/audit/metrics.go`、`GET /api/admin/audit/metrics` 已在 Head `dfbc2955` 的 [Actions #162](https://github.com/Kasign/llm-gateway/actions/runs/40256844693) 全量通过，包括单元测试、MariaDB 队列测试、Gateway 故障注入、race、go vet、Go 构建和前端 bundle 校验。
- 新功能计划 `GW-PROV-UI-001`：管理台允许新增/编辑/启用/停用/删除/测试远程 Provider，同时支持通过 `.env` 在部署时预置服务。实现前先定义环境变量与数据库配置的优先级、配置来源标识、热更新/重启行为、密钥不回显与持久化保护、连接测试与 URL 安全校验；此功能目前尚未实施。
- `GW-REL-001` 已在 Head `fd720204` 的 [Actions #163](https://github.com/Kasign/llm-gateway/actions/runs/40257657819) 全量通过：NXDOMAIN→502、Resolver timeout→504，两条审计记录 `upstream_http_status IS NULL`，敏感 DNS 诊断/凭据均未泄漏。
- 本次推进 `GW-REL-002`：上游提交 HTTP 200 + 部分 SSE 后，测试服务器触发 TCP RST；应验证客户端仍是已提交的 HTTP 200、收到部分 SSE 与 `event: error`，MariaDB 记录 `http_status=200`、`upstream_http_status=200`，但 `status=error/error_code=provider_upstream`。新 Head CI 待验收。
- PR #5 和 PR #7 继续保持未合并；并发上限分支依赖 PR #5。不能把 Mock/受控 Resolver 测试说成真实供应商 E2E。


## 2026-10-26 最新基线与接下来的执行顺序

- 重新核实的远程 `dev` HEAD：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`；工作分支已推进，当前 Head 及 Actions 状态以文末最新核验记录为准。
- PR #5（TCP 故障恢复）和 PR #7（Provider 并发上限）均为 Open、未合并。PR #7 仍基于 PR #5 的契约；#5 合并之前不单独合并 #7。
- 最新 [Actions #167](https://github.com/Kasign/llm-gateway/actions/runs/40260312832) 在 HEAD `9cd736b` 成功；Actions #166 的 32 步 CI 还验证了 race、性能、go vet、Go build、前端 bundle 与 TLS 握手中断。所有结果均为 CI/Mock 层，不等于真实供应商 E2E。
- 可靠性收尾顺序：陈旧 Keep-Alive 连接关闭后的自动重拨专项（本轮新增测试，待新 CI 证明）；补充日志/Payload 保留策略的安全设计（默认禁用破坏性清理）；建立管理台轮询与 DB/CPU/延迟影响的可复现基准。公网代理/跨网络路由仍需部署环境验收。
- `GW-PROV-UI-001` 的首轮实现已提交：部署 YAML 和 `LLM_GATEWAY_PROVIDER_<ID>_*` 预置项只读；Web 项持久化并动态生效；跨来源同 ID 拒绝覆盖；Key 使用独立 AES-GCM 密钥加密存储、API 永不回显；保留/清除 Key 分开操作；模型别名映射接入路由和 `/v1/models`；保留旧 YAML 行为。添加 Provider 不自动切换活动上游。当前继续补齐活动 Provider 清 Key 与编辑禁用的边界保护。
- 不强推、不改写历史；按完整功能组提交。每一组须在 CI 中跑完 Go 单测、race、go vet、构建与前端 bundle，失败查看日志后修复，不削弱断言。


## 2026-10-26 Provider 多来源管理执行更新

- 后端与前端首轮功能已实现：Registry 更新、Web Provider 持久化、AES-256-GCM 密钥加密、部署来源冲突保护、多 Provider 环境预置、可选 `model_mappings`、连接测试接口与 CRUD 部署 bundle。
- 当前 393b14b 的 Actions `40262917490` 全量 CI 已通过，覆盖后端测试、负载/故障注入、race、go vet、构建和前端 bundle。发现活动 Provider 清 Key/编辑禁用保护遗漏，修复后必须重新跑完整 CI；真实供应商 E2E 未执行，不得声明通过。


- 模型路由补充：远程 Provider 只有明确配置的公开别名才覆盖客户端 model；未知 model 仍回退管理台默认值，保留既有兼容。创建新 Provider 若活动上游仍指向同 ID 则拒绝，避免残留上游设置造成创建后隐式激活。

## 2026-10-26 最新基线与安全收敛

- `dev` HEAD：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。当前工作分支提交：`393b14b45a0483e4ab0452acf4c8d283b2567ae5`。PR #5/#7 均 Open、未合并；PR #7 继续依赖 PR #5，不得先合并。
- 当前基线 Actions `40262917490` 已全量通过，包括单测、MariaDB 审计积压、Gateway 负载、429/503、TCP、DNS、SSE、TLS、race、性能、go vet、Go 构建和前端 bundle。下一次提交会触发新验收，不能沿用基线成功标记。
- 安全保护要覆盖所有会使活动 Provider 不可用的操作：删除、停用、显式清 Key，以及编辑保存为 `enabled=false`。必须先检查活动上游并返回 409，且发生在数据库持久化和 Registry 更新之前。
- 已有测试覆盖来源冲突、活动 Provider 的删除/停用、空 Key 保留、密钥加解密及 API 不回显；本轮将扩展活动 Provider 回归矩阵到清 Key 和编辑禁用。
- `.env` 和 Compose 已提供 DeepSeek/OpenAI 默认模型、并发参数和 `MODEL_MAPPINGS_JSON` 显式透传；YAML + `${ENV}` 保持兼容。真实供应商 E2E、公网反代与生产部署验收仍未完成。

### 本地模型长时推理超时（2026-10-26）

- 本地 llama.cpp / Ollama 的整体请求超时通过 `LLAMA_REQUEST_TIMEOUT_SECONDS` 与 `llama.request_timeout_seconds` 配置。显式 `0` 表示不由客户端整体时限主动截断生成；调用方取消或断开仍会通过请求 Context 取消上游请求。
- 兼容旧配置：旧 `config.yaml` 若未提供此字段，仍采用 3600 秒默认值。新示例配置默认使用 `0`，避免慢速本地模型在固定一小时后被网关客户端主动终止。有限时限允许配置 1–604800 秒。
- `warm_start` 的 SSE 注释保活可以帮助长时间等待首个 Token 的流式请求维持连接，但不会改变客户端或反向代理的超时；OpenResty、客户端和调用方自己的请求期限仍需分别确认。
- 本轮已补充配置解析的边界测试代码，遵从统一测试安排未运行测试；不得将代码变更视为已通过运行验证。


### 请求历史自动保留策略（2026-10-26）

- 新增 `logging.retention_days`，默认 0（禁用自动清理），避免升级或首次部署意外删除现有请求历史。
- 显式启用后按 `started_at` 保留期限筛选，只清理已结束请求；仍为 `running` 的请求始终跳过。
- 每批默认 500 条、每轮最多 10 批、每小时运行一次；每个批次使用独立事务，先删 `request_stream_events` 与 `request_payloads`，再删 `request_logs`，降低锁占用与长事务风险。
- 后台任务停止时等待当前批次退出；数据库失败会写错误日志。已新增配置边界和 MariaDB opt-in 集成测试代码但未运行；仍待统一测试清理边界、并发写入、取消与执行耗时。


### 任意 ID 的部署 Provider 环境文件（2026-10-26）

- 主 `.env` 继续显式向 Gateway 传递现有 DeepSeek/OpenAI 示例项，兼容旧配置。
- 新增可选 `providers.env`，由 Compose 单独注入，可以配置任意 `LLM_GATEWAY_PROVIDER_<ID>_*` 变量；默认示例全是注释，不会自动创建 Provider。
- 这个专用文件不会将主 `.env` 中的数据库管理员密码、Redis 密码等无关凭据传进 Gateway 容器。旧部署缺少文件也不影响启动。相关部署与契约文档已同步；按统一安排未执行 Compose/测试验证。


### Provider 响应大小配置透传与本地超时文档（2026-10-26）

- DeepSeek/OpenAI 部署示例与 Compose 环境透传已补齐 `MAX_RESPONSE_BYTES`，可通过 0 使用默认响应体上限，避免环境解析器支持但部署层无法设置。
- 部署配置指南已同步本地模型超时语义：当前示例默认 0 不设置客户端整体时限，旧配置未指定时保留 3600 秒默认；同时明确反向代理与调用方的超时仍需单独设置。
- 按用户统一测试安排未运行 Compose、测试或 CI。


### Provider 级 RPM 限流（2026-10-26）

- 新增 `requests_per_minute`，0 默认禁用，正数按 Redis UTC 固定分钟窗口限制同一个远程 Provider 的新请求数；多个调用方 API Key 和多个 Agent 共用该 Provider 预算，与调用方 Key 自身的 RPM 分离。
- 超限在请求级 ACL 后、WarmStart 和上游转发前拒绝，返回 429 `provider_rate_limit_exceeded`；启用预算时 Redis 不可用采用 fail-closed，返回 503 `provider_rate_limit_unavailable`。
- 该策略只对远程 Provider 生效，不应用到本地 llama.cpp/Ollama，不会中断已经开始的生成。Token 预算与排队继续单独设计，不纳入本次改动。
- 已补充 Redis 隔离和环境变量解析测试代码、管理台配置、部署示例与 API 契约说明；按用户要求未运行测试或 CI。


### Provider RPM 展示和配置样例补齐（2026-10-26）

- Web 管理 API 对部署托管与 Web 托管 Provider 都返回 `requests_per_minute`，保证编辑已有 Web Provider 时会保留其配额；YAML、自定义 `providers.env` 和部署说明均给出 RPM 示例。
- 这是上一项 Provider 级 RPM 的配置链路补齐，不运行测试。


### Provider RPM 用量显示（2026-10-26）

- 管理台 Provider 列表现在显示当前分钟已计数请求数、剩余额度及窗口重置倒计时；按原有“刷新”按钮获取最新快照，不额外增加后台定时轮询。
- 后端采用单次 Redis MGET 读取限流计数，不改变预算、不接触 MariaDB。Redis 查询失败或超时不会阻断 Provider 管理，只将用量显示为暂不可用。
- 已补充 Redis 只读用量测试代码；遵从统一安排没有运行测试。


### Provider RPM 的客户端退避提示（2026-10-26）

- 对未提交响应的 Provider RPM 429 增加标准 `Retry-After` 秒数响应头，指向当前 UTC 固定分钟窗口预计重置时间。
- Chat Completions、Responses 和 Anthropic Messages 共享错误处理函数；不改变状态码、错误 envelope 或 SSE 已提交后的不可逆行为。
- 本次仅完成实现与契约说明，未运行测试。



### OpenResty 长时 SSE 与 Docker 网络部署示例（2026-10-26）

- 修正并明确 OpenResty 反代目标的部署拓扑：宿主机进程使用 `127.0.0.1:8088`；Docker/1Panel 容器必须与 Gateway 共享用户定义网络，使用 `llm-gateway:8088`，容器内不应把 `127.0.0.1` 当成 Gateway。
- 两份样例均关闭 SSE 缓冲、缓存、gzip 及上游自动重试，设置 24 小时读写空闲间隔，明确这些 Nginx timeout 不是整个请求的总时限；请求体大小与 Gateway 默认 64 MiB 对齐。
- 同步 1Panel 部署说明，列明本地模型客户端整体时限为 0 时只取消 Gateway 客户端整体 deadline，不能覆盖 OpenResty 或 Codex/Cursor 客户端期限；同时提示只有在确认真实代理来源后才应设置 `server.trusted_proxies`。
- 本功能组仅修改部署样例和文档；按用户要求未运行测试、Compose 检查或 CI。需要在实际 1Panel/OpenResty 容器网络中部署确认 DNS 解析与 SSE 流式行为。
- 提交前远程基线：`6962c7f84d8d30444424734ec510e89aa9c59dad`；PR #5/#7 均为 Open、未合并，PR #7 继续依赖 PR #5。


### Provider 并发实时占用展示（2026-10-26）

- Provider 管理列表复用现有手动刷新请求，增加并发许可使用数、有效上限与剩余槽位；不另起轮询，也不将只读统计用于限流判定。
- 观测计数来自同一进程内共享的 `ConcurrencyLimiter`。Web Provider 即使在热更新或停用后仍有旧请求在途，也优先读取原有共享 limiter；部署来源从 Registry 获取当前运行时 limiter。若运行实例不可用，UI 显示为不可用而不是伪报 0。
- API 契约明确计数是当前 Gateway 进程的瞬时快照，多副本部署下不代表全局并发；RPM 仍通过 Redis 单独计算，二者不会混用。
- 新增并发快照回归测试代码；同步管理台源码与 `web/static/dist/providers-admin.js`、表头、列数和契约文档。按用户要求未运行测试或构建；需要统一验证 Go 测试、前端 bundle 字节一致性以及热更新/并发请求场景。
- 提交前远程基线：`808fd9f8b399b8ac87885d6e876a935107740a5d`；PR #5/#7 仍 Open、未合并。


### Provider 并发拒绝累计指标（2026-10-26）

- 并发许可器在达到上限并拒绝新请求时累加进程内计数；管理台沿用原有 Provider 列表刷新，展示“并发拒绝（累计）”，不添加定时轮询，不影响请求准入。
- 计数覆盖当前 Gateway 进程生命周期，进程重启后归零；多副本部署按进程分别统计。该值仅代表并发限制触发次数，不代表所有 503、上游 429 或客户端重试次数。
- 自动 CI 日志此前多次失败于 `TestProviderRPMUsageReadsWithoutIncrementing`：测试使用了带大写字母的 Provider ID，写入路径直接拼接原始测试名，读取 API 则按 Provider 契约转小写。调整测试夹具使 ID 在写入前规范化，不改生产限流逻辑，也不放宽断言。
- 更新 Provider 列表 API 契约、管理台源码及部署 bundle；提交前源码和 bundle 完全一致。按用户要求未运行测试、构建或 CI。
- 本次提交前 HEAD：`f09d044bebf285f562bc16c29eb30189da58afd3`；PR #5/#7 仍 Open、未合并。


### 调用审计 Provider 身份字段（2026-10-26）

- `request_logs.provider_id` 记录每条调用的 Provider ID，与 `model_requested`、`model_actual` 分开保存，避免不同供应商使用同名模型时无法按来源统计延迟、成功率和 Token 用量。
- 请求创建时先记可信路由/活动上游的候选 ID；`StageModelResolved` 收到 Resolver 的实际选择后以最终 Provider ID 覆盖，并更新实时看板；请求结束时再次补全。Agent 固定 Provider、内部 ServiceModel、全局活动 Provider 按 Resolver 优先级区分，不能只读取全局活动 Provider。
- 新库在 `request_logs` 创建 `provider_id` 及 `idx_provider_started(provider_id, started_at)`；旧库通过 `ensureColumns` 增量添加，重复列/索引错误按 MariaDB 契约忽略。既有历史记录不伪造来源，保持空值。
- 调用记录列表与详情新增 Provider ID 展示；同步前端源码和 `web/static/dist/app.js`。新增 Provider ID 选择优先级测试代码，但按用户要求未运行测试、迁移或构建。
- 实施前核实 HEAD：`f981d38e66f3d2c0c4eb3906432f13432a867798`；PR #5/#7 均 Open、未合并。


### 实时请求列表 Provider ID 合并修正（2026-10-26）

- 请求列表会合并数据库记录与 LiveRegistry 的运行中快照。补齐 `mergeLiveIntoLog` 对非空 Provider ID 的覆盖，保证已解析的 Agent/服务路由身份不会被较早写入的临时归属覆盖。
- 增加合并优先级回归测试代码；遵从统一安排未运行测试。

### 调用记录 Provider 维度筛选（2026-10-26）

- 管理台增加自由输入的 Provider ID 筛选框，适配静态配置、环境变量和 Web 管理配置中的自定义 ID，不维护容易过期的硬编码列表。
- `GET /api/requests` 新增可选 `provider` 参数，与原有模型、状态、Key 筛选相互组合；ID 去空格并规范为小写后做完整相等匹配，不按模型或 Provider ID 子串猜测归属。
- 存储层增加 `QueryWithProvider`，保留原 `Query` API 作为兼容包装；创建/结束请求时规范化 Provider ID。查询直接比较 `provider_id`，兼顾精确语义和现有索引设计。
- 列表缓存键包含 Provider ID，防止跨条件污染；实时请求先合并 LiveRegistry 的最新 Provider 身份再过滤，避免请求解析前后 Provider 归属变化造成结果残留。
- 已同步 `web/js/features/requests.js`、`web/index.html`、`web/static/dist/app.js`、API/数据库契约、技术指导和问题台账。新增 Provider ID 大小写、空白和精确匹配回归测试代码。
- 遵从用户要求未运行测试、构建、迁移或 GitHub Actions；该功能组为代码和文档已提交状态，不代表运行验收完成。

### 调用统计与 Token 来源拆分（2026-10-26）

- 新增管理员接口 `GET /api/requests/stats`，仅允许 24h、7d、30d 窗口，并可选按 Provider ID 精确过滤。
- Store 层使用 MariaDB 聚合查询计算请求总量、已结束/成功/错误/取消/进行中、成功率、平均耗时和六档延迟分布，不将整个时间窗口的明细行加载到进程内存。
- Token 统计按 `analysis_json.throughput.source` 拆分上游 usage、llama.cpp timings 与来源未知；旧记录和缺失来源不推测为供应商实际 usage。注意分析 JSON 异步落库时会存在短暂的未知来源过渡态。
- 调用记录 UI 增加统计时间窗口、汇总卡片和延迟分布；统计窗口只按 Provider 过滤，不套用模型/状态/Key 明细筛选，避免界面范围含混。
- 新增时间范围参数回归测试代码；已更新 API 契约、技术指导、实施计划和问题台账。按用户要求未运行测试、构建或 CI；MariaDB JSON 函数兼容性、聚合查询性能和 Token 来源数值仍待统一验证。


## 2026-10-26 执行增量：机器可读 API 契约首版

- 新增 `docs/openapi.yaml`（OpenAPI 3.1.0），首批描述健康检查、公共推理协议入口、已配置 Agent 前缀入口、调用统计和 Web Provider 管理 API。
- 规范显式标记为 partial；暂不为未逐项核对的历史管理 API 编造 Schema，也未加入会误导使用者的 Swagger UI 或“自动生成”声明。
- README、API/配置/数据库契约及技术指导已增加入口和维护边界。当前仅同步文档，没有运行测试、构建或重新触发 CI。
- 后续功能组：逐步补齐 API Key、模型下载/运行时、Benchmark、推理参数、登录/会话接口的字段与认证契约；所有路径盘点完成后，再增加 OpenAPI 差异检查。该工作项仍未完全关闭。


## 2026-10-26 执行增量：OpenAPI 管理凭据与 API Key 契约

- `docs/openapi.yaml` 扩展覆盖 `/api/admin/keys` 列表/创建、单 Key 更新/删除、启用/禁用/轮换，以及 `/api/auth/login`、`logout`、`me`、`credentials`。
- 新增 `AdminSessionBearer` 与 `X-Admin-Key` 安全方案，并与推理入口的 Gateway Key、供应商上游 Key 明确区分；创建/轮换明文 Key 被标记为 writeOnly 并注明只显示一次。
- 本次仍按已有代码核实端点，不宣称规范已覆盖全部管理 API；模型、下载、运行时、Benchmark 和推理参数接口仍是后续分阶段工作。未手动运行测试或重跑 Actions。


## 2026-10-26 执行增量：请求审计与运行时 API 契约

- OpenAPI 新增请求日志列表/清理、单条请求详情、审计队列指标、模型列表、运行时概览/预览/选择/卸载和异步 Benchmark 创建/轮询。
- 对含敏感 payload 的请求详情、会清理历史数据的 DELETE、内存态审计指标与进程内 Benchmark job 均记录边界；运行时 schema 保持宽松，避免把多后端异构字段误写成稳定契约。
- 顺手纠正 OpenAPI 一次性返回的 Key 和登录 Token Schema 方向：它们是服务端输出字段，应标记为 `readOnly`；输入密码和 Provider Key 仍使用 `writeOnly`。本轮没有运行测试或重跑 Actions。
- 仍未覆盖：模型安装/下载、HuggingFace 搜索、Ollama 管理、完整上游/Bootstrap、推理参数及配置 API；待按 Handler 逐组补齐，并后续讨论自动契约差异检查。


## 2026-10-26 修正：Benchmark 的零时限语义

- 代码核实发现普通推理配置允许 `request_timeout_seconds: 0` 禁用整体时限，但 Benchmark 后台任务仍无条件执行 `context.WithTimeout(..., 0)`，使任务立即到期。
- 新增共享的可选时限 Context helper；正数才创建 Deadline，0 改为可取消但不设 Deadline 的 Context。用户取消任务仍经父 Context 传播。
- 补充了零值、正值和父 Context 取消的回归测试代码；遵从统一测试安排，本次未运行测试、构建或重跑 Actions。

## 9. 最新执行状态与回归闸门（2026-10-26）

> 本节作为当前进度的权威更新；前文中标注为“最新通过”的旧 Head 和 CI 记录均为历史结果，不代表当前工作分支已通过回归。

### 当前基线

- 开发基线 `dev`：`950f91fe42505b493f9dd4124dbcf30f2b6e4e7a`。
- PR #7 工作分支 `feat/provider-concurrency-limits-stacked` 当前 Head（修复前）：`bd41c549511c700df2b3ba44cb64ca9ef5f1b4f8`。
- PR #5 TCP 恢复测试仍为 Open / 未合并；PR #7 依赖 PR #5 提供的 `core.Result.UpstreamStatus` 契约，不应提前合并。
- 当前 Head 的 [Actions #208](https://github.com/Kasign/llm-gateway/actions/runs/40286112907) 在单元测试阶段失败，后续集成和构建步骤被跳过。本轮没有手动运行测试。

### 先完成当前 CI 阻塞

1. 在 `provider_runtime_test.go` 的 `core.State` 测试构造中，把路由和服务模型字段放入嵌入的 `core.Request`；不修改生产结构体以迁就测试。
2. 修正 MariaDB 审计测试夹具：新表直接定义 `provider_id`，同时对复用的旧测试表进行字段存在性检查并仅在缺失时补列；不降低请求行数与队列排空断言。
3. 在同一个逻辑提交中更新问题台账和本计划。按用户统一回归的安排，不手动运行本地测试；提交触发的新 Actions 将负责全量测试、MariaDB、并发/故障、race、性能、静态检查、Go 构建与前端 bundle 检查。新运行完成前状态保持“待验收”。

### 当前功能组之后的顺序

1. 收口 Provider 管理与运行时预算：验证 Registry 更新失败的配置回滚、活动 Provider 禁用/清 Key 保护，以及热更新期间共享并发许可器的预算语义；保留配置来源边界和密钥不回显。
2. 收口请求观测与 API 契约：核对统计与 Token 来源口径、请求日志 Provider 筛选、审计队列指标，以及 OpenAPI 已覆盖/未覆盖端点；后续增加契约差异检查前先完成端点清单。
3. 功能冻结后进行一次整体代码审查，重点检查请求取消/结束顺序、SSE 状态已提交后的错误语义、超时传播、密钥脱敏、MariaDB 迁移和前端源码/bundle 同步。此阶段只登记与修复实质问题，不为了形式重复跑局部测试。
4. 分支整合采用两次完整回归闸门：A）当前完整功能组提交后，以对应 Head 的 Actions 为准；B）PR #5 合入最新 `dev`、PR #7 与目标基线的最终差异确认后，再完整回归一次。PR #7 未通过最终回归前不合并。
5. 清理已合并的旧分支前，逐个比对内容是否已进入目标分支；旧 PR #6 对应的 `feat/provider-concurrency-limits` 先确认所有独有提交已被 PR #7 覆盖。保留 `main` 和 `dev`，默认分支策略另外决策。

### 进度口径

本轮仅修改两个测试夹具及同步文档，没有手动执行测试或构建。新 CI 完成前，不将这些修复记为已验收；真实供应商 E2E、公网反向代理链路和生产容量验证仍属于部署环境验收，不由 loopback Mock CI 替代。

## 10. 最新执行增量：Provider 管理台语法修复

- Actions #209（Head `b36451e612d7e6a4bdf68535d33e9794604f0690`）验证了此前 CI 阻塞已解除：Go 单测、MariaDB 审计负载、Gateway 故障注入、TCP/DNS/SSE/TLS、race、性能、go vet、Go build 均通过。
- 最终前端契约检查仍失败：`web/js/providers-entry.js:60` 表格 HTML 拼接字符串少了结尾引号，导致 Node.js 语法检查失败。
- 本功能组仅修正这一处语法并同步 `web/static/dist/providers-admin.js`，其余功能不随意扩展；部署 bundle 必须与源码字节一致。
- 未手动运行本地测试/构建。新 Head 的完整 Actions 是本次验收闸门；成功后才进入 Provider 管理功能收口和整体 review。
- 分支纪律不变：当前不清理旧分支；PR #5 先于 PR #7 合入。PR #5 合并后核对 PR #7 与最新 `dev` 的差异，完成第二次完整回归后再讨论 PR #7 合并。

## 11. 最新执行增量：减少请求审计重复写入

- Actions #210 的业务请求全部成功，但 12,192 个请求只写入 9,146 行，日志显示 Writer 在持续压测过程中大量丢弃排队任务，证明当前测试不仅是语法问题，还暴露审计写放大。
- 本次通过结构性合并降低关键队列压力：移除对 `CreateRequest` 已包含的 `model_requested/stream` 再做一次更新；在 Resolver 阶段把 Provider ID 与实际上游模型合并为一个持久化任务。每个推理请求由五个关键生命周期写任务减为三个，避免仅通过扩大队列来掩盖写入能力问题。
- 新增负载验收 Provider 归属计数，原有请求总数、成功状态、实际模型字段的严格断言全部保留；前端语法修复与部署 Bundle 已在当前基线前完成。
- 当前提交将包含代码、回归断言、中文技术说明与问题台账更新。按用户的统一测试安排，不手动运行测试；以新 Head 完整 Actions 的结果作为验收依据。
- 下一功能批次继续检查 Provider 更新/回滚、活动 Provider 清 Key/禁用保护与并发许可器预算；在当前负载验收转绿前不扩展新的运行时行为，不调整 PR #5/#7 的依赖顺序。

## 12. 最新执行增量：主管理台 Bundle 同步

- Actions #211 已证明上一批审计写放大修复有效：Gateway 负载与请求审计、MariaDB、错误注入、race、静态检查和 Gateway 构建阶段均通过；失败仅发生在最终 `app.js` 与当前源码构建结果的字节比较。
- 本次按 CI 差异同步 `web/static/dist/app.js` 中的请求统计 UI 编译产物。保留真实源码和既有 Bundle 校验，不使用“跳过 cmp”或 CI 自动覆盖工作树的方式掩盖不同步。
- 当前功能组提交后重新运行完整 Actions；新的结果完成前不宣称整体回归通过，不再叠加其它功能提交。

## 13. 下一功能批次：管理 API 错误边界与 OpenAPI 精确性

- 将请求统计 MariaDB 聚合失败时的底层错误保留在服务端日志，不回显给管理员 API 客户端；用单测验证通用错误响应的状态码、错误码和诊断文本隔离。
- 将 Provider 创建/删除的 OpenAPI 成功响应修正为与 Handler 一致的 `201 Created` 和 `204 No Content`，并明确删除没有响应正文。
- 本批独立于审计吞吐与前端 Bundle 修复；先完成当前 Head 的完整 Actions，再推送此功能组。新 Head 若失败，只围绕实际失败日志修复，不改变已有负载断言或关闭契约检查。

## 14. Provider 并发热更新顺序修正

- 当前 Review 确认：限制下降发生在配置持久化之后，会留下一个很短的旧限额准入窗口；编辑已停用 Provider 时，下降分支也可能意外修改共享运行时 limiter。
- 调整顺序为：先准备并立即应用下降，再持久化；若 MariaDB 持久化失败则回滚 limiter；Registry 更新失败仍同时回滚 limiter 与持久化配置。提高限额必须等 Registry 更新成功后才生效。
- 编辑已停用 Provider 不改变当前 limiter，即使保存了较低的新配置；重新启用时才将新配置应用到运行时。新增边界测试验证 disabled edit 不上调、也不下调在途请求可能共享的许可器。
- 本功能组与统计 API 错误脱敏、OpenAPI 状态码校准合成同一轮整合回归；只有当前 Head 的完整 Actions 通过后才更新工作分支。

## 15. 管理端点 OpenAPI 覆盖扩展

- 已按实际 `Server.Handler` 注册与 Handler 请求/响应实现，补入此前缺失的 HF 文件/搜索、模型安装和兼容操作、下载任务清理、Bootstrap、Backend 查看/切换、批量卸载、上游模型列表、目录搜索/Ollama tags、推理参数动态 Schema、Ollama Pull/模型动作、本地库存与统一 model-ops 等契约。
- 补充可复用 Schema，描述稳定字段和异步任务边界；运行时、目录搜索卡片、推理参数和上游透传 JSON 保留扩展字段，避免过度承诺实现没有固定保证的属性。
- 保持 `x-contract-status: partial`。后续需要把所有 `/api/*` 注册路径和 OpenAPI path 建立自动差异清单，再按 handler/schema 逐项收口；当前仍不声称契约已全覆盖或自动生成。
- 当前批次更新 OpenAPI 与技术指导、问题台账、实施计划。按统一回归规则，不手动运行测试；提交后观察新 Head 的 GitHub Actions。

## 16. OpenAPI 第二轮精确性与高风险路径 Review

- 补齐 Agent 固定 Provider 的所有已支持协议路径：models、Chat Completions、Completions、Responses、Anthropic Messages 和 Messages count_tokens。Agent 使用固定 Provider，不等于可由请求体任意切换后端。
- 将 Ollama Library tag 与可用 Backend 的已知 JSON 字段从泛型对象升级为显式属性；运行时快照/目录卡片/动态推理参数仍允许扩展字段。
- 第二轮高风险 Review 覆盖请求取消与 Finish 竞态、SSE 响应已提交后错误处理、审计 FIFO/排队丢弃指标、管理员认证与 Provider 共享并发许可器。当前未发现需在此批引入新的运行时代码改动的问题；仍须靠当前完整 CI 验收。
- 当前 OpenAPI 仍为 partial。后续重点是建立 Handler 路由与 OpenAPI path 自动对账，避免新增管理端点时只靠人工台账；此项先在回归通过后按独立功能规划。

## 17. OpenAPI 路径集合自动对账

- 将 `POST /api/requests/{id}/cancel` 的实际 Handler 契约补齐到 OpenAPI，明确取消在途请求、收口遗留运行记录及 409 竞争状态。
- 新增 Python 标准库脚本读取 Go 路由注册和 OpenAPI 顶层 paths，并用明确、可审查的映射展开动态 Go 前缀路由；把该检查放到 Actions 的前置步骤，尽早发现路由漏文档。
- 对账采用双向集合比较，同时发现漏写 path 和已删除路由留下的旧 path。动态路由系列不使用通配忽略，而用显式路径清单映射。
- 该门禁只证明路径键对应，不等于 JSON Schema/HTTP method/response code 自动验证；OpenAPI 继续保持 partial。先待 Actions #215 对 `dba3892` 的完整回归结束，再推送该批候选提交。

## 18. 处理 PR #7 未解决的测试清理 Review

- 自动 Review 指出 Provider 并发测试首请求等待启动超时后直接 Fatal，mock handler 可能仍阻塞在 `releaseFirst`，使 `httptest.Server.Close` 一直等待。
- 在该 timeout 分支先释放 mock handler，再结束测试；错误路径不再依赖请求恰好没到达 handler 的调度假设。成功路径和并发/上游请求数量断言保持不变。
- 该变更属于测试基础设施可靠性的小修复，和生产并发逻辑无关。先等待当前 Head 的完整 Actions，再将修复和中文问题台账作为一个逻辑提交推送。

## 2026-10-26 配置治理第一批：模板对账与环境变量契约门禁

- 将 config.yaml 的有效字段路径与 config.example.yaml 对齐，补入日志保留默认值与 agents: []，并将本地请求超时改为引用 LLAMA_REQUEST_TIMEOUT_SECONDS；DB/Redis 地址也已改为环境变量引用，Compose 继续提供 `Mariadb` 与 `Redis:6379` 默认回退以兼容已有 .env；配置检查器不允许两份 YAML 存在未登记静态值差异。
- 新增 scripts/check_config_contract.go，使用现有 yaml.v3 依赖反射检查 YAML 字段是否存在于 Go 配置结构，比较两份 YAML 的有效字段路径与静态默认值，校验 YAML 环境变量在 .env.example 声明并由 Compose 白名单注入，同时对账 Provider 环境变量解析器、示例文件与 DeepSeek/OpenAI Compose 字段。
- 将配置契约检查加入 GitHub Actions；新增 docs/configuration-matrix.md 作为配置主维护入口清单，并更新中文配置说明、README、问题台账与本计划。
- 按本轮约定没有手动运行测试或重放负载。提交后需查看新 Head 对应 Actions；真实部署兼容、Provider 跨来源冲突、慢模型超时和审计负载仍留在统一回归清单中。
- 数据库和 Redis 的服务地址已移至 `LLM_GATEWAY_DB_HOST`、`LLM_GATEWAY_REDIS_ADDR`，由 Compose 显式注入，并为既有 `.env` 缺少新变量提供兼容默认值；两份 YAML 的静态字段值现在要求完全一致。随后继续 Review Web Provider 的跨来源冲突/热更新和剩余运行治理功能。当前不删除 config.yaml，以免给现有部署升级带来缺失挂载风险。


## 2026-10-26 配置治理第二批：将数据库和 Redis 地址移入环境层

- 将 `database.host` 与 `redis.addr` 改为引用 `LLM_GATEWAY_DB_HOST`、`LLM_GATEWAY_REDIS_ADDR`，并在主 `.env.example` 和 Compose 显式环境白名单中声明。
- Compose 对已有 `.env` 缺少这两个新变量的部署继续回退到 `Mariadb` 与 `Redis:6379`；因此旧环境不需要立即补变量即可沿用既有容器名。新安装可按实际 Docker network 内的容器名称修改示例值。
- 配置契约检查不再保留 `database.host` / `redis.addr` 两个差异例外；两份 YAML 的静态字段和值必须完全一致。完整 Actions #223（运行 `40302076802`，34/34 步）已通过。


## 2026-10-26 OpenAPI operation 元数据门禁

- `docs/openapi.yaml` 当前包含 63 个字面路径模板与 72 个 HTTP operation；四条配置 Agent 前缀展开后，CI 核对 67 个运行时路由路径。所有 operation 都有唯一、稳定的 `operationId`，方便 SDK 生成和管理 API 的引用。
- `scripts/check_openapi_routes.py` 在原有路径双向对账之外，新增 operationId 唯一性、summary、responses 和显式 security 检查；健康检查和登录是仅有的公开操作，其余 operation 必须有安全契约。完整 Actions #224（运行 `40302664234`，34/34 步）已通过。
- 该门禁确保 operation 元数据完整，但不宣称所有 JSON Schema 已与 Go Handler 序列化完全等价；字段级契约继续依实际 Handler 与响应类型分批对照。

## 2026-10-26 请求日志保留可观测性

在现有分批删除逻辑上增加管理员只读 API `GET /api/admin/retention/status`，公开最近运行状态、耗时、删除量、成功/失败计数和稳定失败代码，支持外部监控器轮询。指标只存当前进程内，重启后计数归零；原始数据库错误只写服务日志，不经 API 返回。此功能不增加通知服务，也不声称主动推送告警。

同时同步 OpenAPI 响应结构及状态契约测试。完整 Actions #225（运行 `40303724984`，34/34 步）通过，包含新增 Go 状态聚合单测、路由/OpenAPI 检查、配置契约、全套集成负载、race、构建和前端 Bundle。


## 2026-10-26 Provider 每日 Token 预算（第一版）

- 新增远程 Provider `token_budget_per_day`，并支持 YAML、任意 ID 环境预置及 Web 管理台配置；0 代表关闭，正数在 UTC 自然日内对同一 Provider 的所有调用方和 Agent 共享。
- 准入前按请求体 UTF-8 字节数与单次最大输出 Token 数做估算预占。Redis Lua 原子预占处理并发；预算启用时，若可用额度更低，会尽量收紧本次输出上限。估算不等于第三方 tokenizer 或计费；真实 usage 超出预占时会记入真实值并影响后续请求。
- 有可信供应商 usage 时按真实总 Token（或 input+output）结算并释放预占差额；没有可信 usage 的已出网上游请求会保守保留预占直到日桶过期。不会把 llama timings 或估算值冒充供应商计费 Token。
- 新增 Redis UTC 日桶预占/结算用例、输出上限字段用例、Web 配置和用量展示、环境变量白名单、OpenAPI 和中文配置说明。本批未手动运行测试，等待新 Head GitHub Actions。
- 排队策略仍独立待设计，需要另外评审公平性、队列上限、等待超时、取消移除、Redis 故障策略和 SSE 提交边界。


## 2026-10-27 Provider 有界 FIFO 排队

- 在已有 Provider 并发许可器内实现可选的有界 FIFO 等待队列，避免在 Provider 热更新后丢失共享准入状态。队列关闭（默认 `max_queue_size=0`）时继续 fail-fast 返回 503，不改变旧部署行为。
- 队列容量允许 0–1024；启用后队列满返回 `provider_queue_full`，等待超时返回 `provider_queue_timeout`（503），客户端取消会移除等待项。发放许可时按 FIFO 预留并发槽位，并在等待者取消时归还槽位。
- Provider 配置新增 `max_queue_size` 与 `queue_wait_timeout_seconds`，同步 YAML、任意 ID 环境变量、Docker Compose 显式白名单、Web 管理 CRUD、安全 Provider 视图、用量指标和 OpenAPI Schema。Web 热更新遵守并发预算相同的事务顺序：缩小队列先应用，扩大队列需等持久化和 Registry 更新成功。
- 管理台同时展示活动并发、等待队列深度、拒绝数量与队列超时数。此队列及计数只在单个 Gateway 进程内生效；RPM 与每日 Token 预算仍走 Redis，不把本功能描述成多副本全局排队。
- 新增 FIFO 顺序、请求取消、队列满、等待超时和配置范围用例。依当前协作约定，本次不在修改阶段重复跑手动测试；提交后以 GitHub Actions 为准，真实慢上游与反向代理端到端行为仍留给统一部署回归。


## 2026-10-27 高风险管理 API 契约门禁

- Provider FIFO 队列的完整验收已通过 Actions [40434515661](https://github.com/Kasign/llm-gateway/actions/runs/40434515661)，Head 为 `9533cfd51dbd1fc789fcc8480a65c5ba10990534`；真实供应商、公网代理和生产容量仍留待统一部署回归。
- 复核管理 API 后，统计接口的数据库失败已使用通用 `database_error` 响应并将原始原因只写服务端日志；Provider 创建/删除 Handler 分别返回 `201 Created` 与无正文的 `204 No Content`，已有单测覆盖统计错误脱敏。
- 新增 `scripts/check_openapi_contracts.py`，在 CI 前置阶段核验 Provider 配置/用量 Schema 的关键字段和范围、创建/删除响应正文边界及统计接口稳定错误封套。同步补齐 ProviderView 中已由 Handler 返回但先前未显式声明的并发使用指标。
- 本门禁只覆盖高风险子集；`x-contract-status: partial` 保留，不声称已完成所有 Handler 的全字段 Schema 对账。后续逐步扩展到请求统计 Token 来源、请求日志 Provider 筛选与取消端点。


## 2026-10-27 FIFO 队列截止时刻复审修正

- 完成第一轮 FIFO 队列提交的完整 Actions 后，复审发现定时器与并发许可同时就绪时存在边界窗口：等待 goroutine 若延迟恢复，可能在 deadline 之后接受许可。
- deadline 改为在等待项进入队列时固定，并为每个 waiter 记录实际许可发放时间；ready 和 timeout 分支都检查时间戳。晚到许可会退还并计为一次队列超时，父请求取消仍优先返回 Context 错误。
- 新增锁竞争确定性回归用例覆盖晚到许可，不修改既有 FIFO、队列容量、取消和故障注入语义。该补丁提交后的完整 Actions 是本轮验收闸门。

## 2026-10-27 根目录部署迁移后的配置路径 Review

- 部署入口已统一到仓库根目录：`.env.example`、`providers.env.example`、`config.yaml`、`config.example.yaml`、`docker-compose.yml` 和 1Panel / OpenResty 说明不再要求进入旧的 `deploy/` 目录。
- 修复配置启动错误中残留的旧路径提示：当两个本地后端均未配置，或当前活动后端地址缺失时，提示改为仓库根目录的 `.env`。该修改只纠正运维指引，不改变 URL 校验和活动后端选择语义。
- 环境变量展开针对完整、未加引号的 `${VAR}` 标量恢复 YAML 原生整数/布尔/null 标签，避免 `LLAMA_REQUEST_TIMEOUT_SECONDS=0` 被按字符串解码；带引号的值仍强制保持字符串，避免数字型密码或 Key 被意外转换。
- CI 已包含静态配置契约、根目录 Compose 渲染与 Release 镜像构建步骤。当前提交的 GitHub Actions 运行状态尚未从状态接口取得，不能用之前 Head 的绿色结果代替；实际 1Panel 容器网络、已有本地配置迁移、慢模型 SSE 和公网反向代理仍需部署环境验收。
- 后续工作按原计划继续：完成 Provider 热更新与配置回滚的高风险 Review，逐项检查 OpenAPI 与管理 Handler 的字段/状态码契约，再做一次集中代码 Review。真实供应商凭据及生产请求不在 CI Mock 测试的证明范围内。

## 2026-10-27 根目录配置加载回归的 CI 修复

- 新增的 `TestRootConfigLoadsWithZeroLocalRequestTimeout` 首轮 Actions 在 Go 编译阶段失败，唯一诊断为 `expandenv_test.go` 中闭包捕获写成重复短变量声明（`no new variables on left side of :=`），不涉及生产配置解析。
- 已将闭包副本改为独立变量名，保留清理阶段恢复原 Provider 环境变量的目的；静态配置契约和根目录 Compose 检查在该次 Actions 中均已通过，单元测试失败导致后续步骤被跳过。
- 修复后的新 Head 需要重新执行完整 Actions。只有新 Head 全量通过后，才将根目录实际配置加载与超时零值回归标为验收完成。

## 15. 管理台本地模型列表与运行时状态隔离（2026-10-27）

- 代码 Review 发现本地模型页的 `modelHub.refresh` 通过 `Promise.all` 同时等待数据库模型清单和当前上游运行时快照；任何一个运行时读取错误都会阻断整个页面渲染。
- 修复原则：本地清单以 `/api/models` 结果为准，运行时信息只是附加状态。使用 `Promise.allSettled` 分别更新两类数据；后端状态异常不能隐藏已登记的 GGUF 清单，也不能把“运行时接口失败”显示为“没有加载模型”。
- 清单请求失败时，首次加载显示独立错误；若已有成功快照则保留旧数据并明确提示刷新失败。所有错误向用户显示稳定通用信息，不透出上游异常正文。
- 当前修复已同步源码、部署 bundle 与问题台账。最新 Head 的 CI 完成后确认构建与 bundle 一致性；真实部署服务故障恢复由用户后续统一回归。

## 16. 部署托管 Provider 队列配置运行时透传（2026-10-27）

- 对照配置矩阵与实际运行时构造路径发现，Web Provider 使用 `buildRemoteProvider`，已传递 FIFO 队列配置；YAML/环境 Provider 则在 `newInferencePipeline` 里直接构造，遗漏了队列容量和等待时限。
- 已补齐启动注册字段透传，并增加 Server 层回归覆盖配置后的队列上限和等待超时语义，避免仅通过 Config.Load 静态校验却在运行时退回默认值。
- CI 通过后关闭此代码级问题。供应商真实连接、代理读超时和线上负载仍需要在统一部署回归中验证。

## 17. Provider 预算预占与 FIFO 准入状态分离（2026-10-27）

- Pipeline 的 `UpstreamStart` 表示即将进入 Provider Forward，并不表示已经通过 Provider 并发/队列准入。Review 确认旧逻辑在队列满、等待超时或 fail-fast 时仍会将预算预占视为“可能已计费”并保留到日桶过期。
- 增加可选 `OnUpstreamDispatch` 回调，由 OpenAI-compatible Provider 在排队许可通过、即将调用出站 HTTP 时触发。预算 reservation 自身记录 `Dispatched`，不再依赖通用 Pipeline 阶段时间戳推断网络行为。
- 未 dispatch 的请求结算为 0 并退还预占；已 dispatch 但缺少可信 usage 的请求仍保守保留；有可信 usage 时按真实 usage 结算。补充 Provider 并发拒绝/排队超时的回调回归和 Redis 服务端结算回归。
- 该规则只调整 Gateway 自己的 Redis 预算，不自动判断第三方在网络错误后是否计费；真实供应商账单仍必须以可信 usage / 供应商平台数据为准。

## 18. 本地模型列表响应 Schema 与 Go 字段对账（2026-10-27）

- `GET /api/models` 的 Handler 返回 `ListGatewayModelRows` 生成的模型记录，而 OpenAPI 路径曾使用泛化 object，未消费现有 `GatewayModel` Schema。
- 已改为引用 `GatewayModel` 并扩充标准库契约检查器：将 `model.Model` 的 JSON tag 与 Schema 属性对账，同时检查该 Handler 新增的 `upstream_llamacpp_id`、`upstream_ollama_name`、`upstream_name`、`disk_bytes` 字段。
- 这是有针对性的字段级门禁，不代表所有管理 API 的 Go/Schema 完全等价；后续应按实际功能风险继续分批补齐，而不把 OpenAPI 的 `partial` 状态提前改成完整。

## 19. CI 并行化与缓存（2026-10-27）

- 将原先单个长 Job 拆为四个独立并行 Job：配置/API 契约与前端、Go 测试与 race/vet、Gateway 负载/故障注入、Go/Docker 构建。
- 保留 Redis/MariaDB 覆盖、队列/预算回归、故障注入、Race、Compose、前端 Bundle 比对和 Release 镜像构建。MariaDB 专项审计测试在全套 `go test ./...` 之外仍保留于负载 Job，便于查看独立诊断和产物。
- 加入同一 ref 的过期工作流取消策略、Node npm 缓存及 Buildx GitHub Actions 层缓存。性能目标是让独立耗时阶段并行，而不是减少必要的回归范围。
- 后续比较本次完整 Actions 的总时长与分 Job 耗时；若 GitHub Runner/服务启动成为瓶颈，再按实测结果调整并行度。

## 20. 请求日志筛选与取消端点响应契约（2026-10-27）

- `cancelRequestAPI` 对非 POST 方法明确返回 405，但 `docs/openapi.yaml` 漏记该状态码；已补齐稳定 AdminError 响应。
- 扩展 `scripts/check_openapi_contracts.py`，要求取消操作具备 200/400/401/405/409 响应定义，并检查 Handler 的方法校验、400/409 分支与成功响应关键字段。
- 对 `GET /api/requests` 的 Provider 筛选做跨层对账，检查 OpenAPI 将其声明为字符串 query 参数，且 Go Handler 确实读取并传入日志合并查询。
- 这项检查不等于对所有 RequestList/RequestDetail 动态字段进行全量 Schema 生成或等价性验证。

## 21. Provider FIFO 队列热更新事务回归（2026-10-27）

- 并发预算已有安全应用顺序测试，队列容量的同类 helper 之前没有等价覆盖。新增测试覆盖缩小前置应用并可回滚、扩大延后应用、已停用 Provider 编辑不修改活动许可器、重新启用后延后放宽，以及容量越界拒绝。
- 该批只补热更新事务的回归覆盖，不改变队列策略；FIFO 队列仍是单 Gateway 进程内状态，不代表多副本全局排队。

## 22. CI 稳态负载与故障注入并行化（2026-10-27）

- 前三轮并行工作流均全量通过，墙钟耗时约 2 分钟；检查分 Job 步骤发现 Gateway Job 内仍串行执行 60 秒、12,000 请求的稳态负载，再依次执行多组故障注入。
- 将其拆为 `gateway-steady-load` 与 `gateway-faults` 两个并行 Job，保留 MariaDB 审计积压、完整稳态负载、429/503、TCP、DNS、已提交 SSE 后断流、TLS 握手及所有 Artifact。
- 两个 Job 各自启动 Redis/MariaDB 测试服务，因此会增加少量 Runner 资源使用。目标是降低等待墙钟时间，而不是减少必要验证；合并前比较新 Actions 实测时间与所有 Job 结果。

## 23. 请求日志 DTO 与 OpenAPI Schema 对账（2026-10-27）

- `RequestList.data.items` 与 `RequestDetail.log` 已改为引用共享的 `RequestLog` Schema。Schema 按 `store.RequestLog` 当前 JSON 字段声明状态、Gateway/上游 HTTP 状态、Token、时间与错误信息。
- 扩展 `scripts/check_openapi_contracts.py`，自动核对 Go `store.RequestLog` 的 JSON tag 全部映射到 Schema，并检查列表/详情两处引用，避免请求日志前后端字段静默漂移。
- 详情里的 summary、timing、analysis、正文与流式事件仍包含动态或条件字段，本批不声称实现全量 API Schema 等价。

## 24. 请求详情 summary/timing Schema 与 Go helper 对账（2026-10-27）

- 将 `RequestDetail.summary` 和 `RequestDetail.timing` 从泛化 object 改为共享、可复用的 `RequestSummary` / `RequestTiming` Schema，列出请求身份/状态、模型与 Provider、请求体积、延迟、TTFT、生成阶段和 Token 来源等稳定字段。
- 扩展 `scripts/check_openapi_contracts.py`，从 `buildRequestSummary`、`buildRequestTiming`、`buildRequestDetail`、`applyHarvestToTiming` 提取 map 键，并在 CI 中验证每个字段都有 Schema 定义。
- 正文与 Header、分析结果、SSE 事件仍是条件字段；这是一项针对详情接口的可维护性门禁，不代表所有 API 已完全 Schema 等价。

## 25. RequestDetail 分析与 SSE 事件 Schema 对账（2026-10-27）

- `RequestDetail.analysis` 引用 `RequestAnalysis`，其阶段耗时和吞吐子对象分别引用 `RequestPhaseMS`、`RequestThroughput`；`stream_events.items` 引用 `StreamEvent`，避免管理台分析/流事件长期停留在任意 object。
- 扩展契约脚本，对照 Go 分析 DTO 和持久化流事件 DTO 的 JSON tags；条件正文/Headers 不标记为必需字段，避免将配置关闭时不存在的数据误写进契约。
