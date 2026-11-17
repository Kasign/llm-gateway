# 当前任务：TOOL-04 SearXNG Adapter

> 状态：已完成
> 更新日期：2026-11-17
> 仓库：Kasign/llm-gateway
> 分支：dev
> 开始基线：5e6732ad1e5992470d927fdfd712f6e6f10f44c6
> 前置任务：[TOOL-03 Runtime 执行边界](任务历史/工具能力/TOOL-03-Runtime执行边界.md)
> 任务状态权威来源：[整体任务规划](整体任务规划.md)

## 1. 任务目标

在统一 Tools 模块内实现第一个具体工具子模块：SearXNG 搜索 Adapter。它实现根包的 Executor 契约，通过服务端固定配置访问 SearXNG JSON API，将结果归一化为受限 JSON，供后续 Coordinator 回传给模型。

目录固定为 `internal/tools/modules/searxng`。它是 Tools 模块内部的独立 Go package，不是独立服务或独立 `go.mod`。

## 2. 架构与安全边界

- 工具名固定为稳定标识 `web.search`，不包含 Base URL、部署名、Provider 或凭据。
- SearXNG Base URL 只能来自服务端配置并在构造时注入；模型参数只允许提供查询词，不能指定 URL、Host、端口、请求头、代理或凭据。
- 固定调用 SearXNG `/search` JSON 接口并强制 `format=json`；不提供通用 URL Fetch。
- 工具子模块只实现 Executor 和自身协议解析；不依赖 Server、Provider、Coordinator，也不调用其他工具。
- Server 后续负责构造模块并向 Registry 注册；本任务先完成 Adapter 本身，不提前实现请求级模型循环。
- 搜索结果属于不可信外部内容，不得改变系统提示、工具授权或执行策略；不自动访问结果 URL。

## 3. 第一版功能范围

- 输入参数：必填 `query`，长度 1–512 个 Unicode code point；第一版不向模型开放语言、类别、时间范围、Base URL 或请求头。
- 配置：Base URL、是否允许明文 HTTP、是否允许私有网络、HTTP 请求超时、最大结果条数、安全搜索等级、默认语言、允许类别、最大上游响应字节数。
- 初始目标：SearXNG 请求超时默认 8 秒；最多返回 5 条；上游响应体限制默认 512 KiB；归一化输出目标不超过 16 KiB，并受 Runtime 64 KiB 通用上限再次保护。
- 输出字段：query、results；每条结果仅保留 title、url、content、engine、score、published_at（仅源数据确实提供时）。
- 字段缺失与空字符串分开处理；不伪造发布时间、评分或来源。
- 错误分类至少区分：超时/取消、上游不可用或非成功状态、非 JSON/结构错误、响应超限、空结果。
- 不自动重试，不抓取结果 URL，不保留完整上游响应正文。

## 4. 现状审查与已确认设计

### 4.1 仓库现状

- `internal/provider/openai_compatible.go` 已有严格的 Provider Base URL 校验、DNS 解析、IP 网段过滤、禁止代理、禁止重定向和响应体上限；其实现属于 Provider 私有代码，Tools 不得依赖 Provider 包。
- Provider 的安全 HTTP Client 不是可直接复用的公共能力。本任务实现 SearXNG 专用 Client，不复制 Provider 的并发队列，也不为单个 Adapter 抽象通用网络平台。
- `internal/config.Config` 当前没有 Tools 配置段；`docker-compose.yml` 的 Gateway 加入外部 Docker network，并提供 `host.docker.internal:host-gateway`。本任务只定义 SearXNG 模块自身 Config，不提前改 Server / 全局 config / Docker Compose；后续 Server 装配任务再注入配置。
- Gateway 容器到真实 SearXNG 的网络连通性尚未验证。宿主机 `127.0.0.1:8080` 已验证可用，但容器内 loopback 指向 Gateway 容器本身，不能直接复用。
- SearXNG JSON 响应的实际字段样本已在 TOOL-01 记录；不重复做宿主机同一接口测试。

### 4.2 HTTP 与 URL 安全决策

1. Base URL 仅在 Executor 构造时传入；模型参数只包含 query。
2. 只接受绝对 HTTP(S) URL；拒绝 userinfo、query、fragment、opaque URL、反斜线和 dot segments。
3. 明文 HTTP 必须显式设置 `AllowInsecureHTTP=true`；私有网络地址必须显式设置 `AllowPrivateNetwork=true`。
4. 始终拒绝 loopback、link-local、multicast、unspecified 和云元数据常用 link-local 地址；私有地址只有显式允许时才可连接。
5. 每次新建连接时解析 DNS，并直接拨号到已校验的 IP，避免校验后再次解析造成 DNS rebinding；不使用环境代理。
6. 所有重定向均不跟随；收到 3xx 按上游状态错误处理。
7. HTTP Client 和请求 Context 双重设置超时；响应体通过 `io.LimitReader(limit+1)` 读取，超过上限立即拒绝。

### 4.3 第一版 Config 默认值

| 配置 | 默认值 | 硬上限 / 规则 |
|---|---:|---|
| RequestTimeout | 8 秒 | 不超过 30 秒 |
| MaxResults | 5 | 1–5 |
| MaxResponseBytes | 512 KiB | 不超过 4 MiB |
| MaxOutputBytes | 16 KiB | 模块固定上限 |
| SafeSearch | 1 | 0–2 |
| DefaultLanguage | 空 | 空表示交由 SearXNG 默认处理 |
| Categories | general | 只使用服务端配置，不由模型选择 |
| AllowInsecureHTTP | false | HTTP 必须显式开启 |
| AllowPrivateNetwork | false | 私有网络必须显式开启 |

输出字段上限按字节控制并保持 UTF-8 完整；默认最多 5 条，单条 title 256 bytes、url 1024 bytes、content 512 bytes、engine 64 bytes、published_at 64 bytes。若归一化 JSON 仍超过 16 KiB，返回明确超限错误，不静默截断。

## 5. 本任务不实现

- Tool Coordinator、模型多轮工具调用、Provider 请求改写和 Pipeline 接入。
- 管理 API、Web UI、持久化配置或数据库表。
- 通用 Fetch URL、网页正文抓取、浏览器自动化。
- 自动重试、并发搜索调度、动态工具加载/卸载。
- 工具权限产品流程与工具调用审计持久化。

## 6. 验收条件

- 仅通过构造时注入的服务端 Base URL 访问 SearXNG；模型无法改变目标地址或请求头。
- URL 配置非法时构造失败；跨 Origin 重定向不被跟随。
- HTTP 请求遵守传入 Context 和 Adapter 超时；取消能够及时中止请求。
- 上游响应体严格受字节上限约束；超限拒绝，不截断后继续解析。
- 仅接受合法 JSON 和预期结构；缺字段可控降级，错误响应可诊断。
- 输出字段归一化、结果条数和摘要长度受限；不伪造字段。
- 输出大小满足模块限制并兼容 Runtime 的 JSON / 大小校验。
- 单元测试覆盖成功、空结果、HTTP 错误、超时/取消、非 JSON、结构错误、超限、重定向和 Base URL 校验。
- 不修改现有推理主流程；全量 Go 测试、竞态测试和 CI 通过。

## 7. 实施结果

1. 已实现 `internal/tools/modules/searxng/searxng.go`，提供稳定 `web.search` Definition 与 Executor。
2. Config 固化 Base URL、HTTP/私网显式授权、请求超时、结果数量、响应体大小、安全搜索、语言和类别。
3. HTTP Client 禁止环境代理和重定向；DNS 每次新连接解析并校验目标 IP，阻止 loopback、link-local、云元数据及未授权私网地址，避免 DNS rebinding。
4. 请求固定 `/search` 与 `format=json`；模型只提供 query。响应体有字节上限，结果 URL 只保留 HTTP(S)，字段按字节截断并保持 UTF-8。
5. 已使用 httptest 覆盖成功、空结果、HTTP 状态错误、非法 JSON、结构错误、超限、重定向、超时/取消、URL 校验、DNS/IP 策略和结果归一化。
6. 未改 Server / 全局 config / Pipeline；Server 装配和真实容器网络验收留给后续任务。


## 8. 最终执行记录与验收

- TOOL-03 已完成，归档见 [TOOL-03 历史记录](任务历史/工具能力/TOOL-03-Runtime执行边界.md)。
- Adapter 初始实现提交：`ae3919ecd53910da8eb4db48f8ffa8fcaea5116e`；URL/DNS 安全与测试修正：`cdbe4400925cee6c451ca5bfc942887a783c0e55`；测试编译修正：`e1422acbaedadbaabacfbf14e395ad5e460ada02`。
- 最终验收分支 HEAD：`e1422acbaedadbaabacfbf14e395ad5e460ada02`。
- Go CI：<https://github.com/Kasign/llm-gateway/actions/runs/44016349241>，结论 success。
- 已通过 `go test ./...`、`go test -race ./internal/tools/...`、`go vet ./...`、Gateway 编译、Docker 镜像构建、配置/API 契约与前端构建、故障注入与恢复、稳态负载及审计持久化。
- 独立 Web UI Browser Smoke 工作流因 Chromium / Playwright 安装步骤失败；本任务没有修改 Web UI，该失败不影响 Go CI 成功结论。
- 真实 SearXNG 宿主机 API 样本已由 TOOL-01 验证；Gateway 容器到 SearXNG 的网络连通性仍未验证，明确留给 TOOL-09，不宣称已完成端到端搜索。
- 未新增全局 config 字段、Server 注册装配、HTTP 路由、数据库或 Pipeline 改动。

## 9. 验收结论

SearXNG Adapter 的独立模块实现和本地协议/安全边界测试已完成。Server 装配、Coordinator 闭环及真实部署网络验收由后续任务完成。