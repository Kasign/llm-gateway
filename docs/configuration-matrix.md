# LLM Gateway 配置清单与来源矩阵

新增配置时同步更新 Go 配置结构、示例模板、Compose 环境白名单（需要注入变量时）、中文说明与 CI 契约检查。远程 Provider 的唯一维护入口是 Web 管理台。

## 1. 服务静态配置

| 配置组 | 主维护入口 | 敏感性 | 生效时机 | 备注 |
| --- | --- | --- | --- | --- |
| server.host、server.port | config.yaml | 低 | 重启 | Compose 默认只将 8088 绑定到宿主机回环地址 |
| server.trusted_proxies | config.yaml | 中 | 重启 | 仅填写实际代理来源 IP/CIDR，不要信任所有转发头 |
| 管理 API Key | .env 提供，YAML 引用 | 高 | 重启 | 与 Web 登录会话及调用方推理 API Key 分离 |
| Web 登录初始哈希/保护开关 | .env 经 Compose 显式注入 | 高 | 首次初始化；已有数据库设置优先 | 控制台保存后持久化在 gateway_settings.web_admin_login |
| database.* | .env 注入地址和凭据，其他结构参数在 YAML | 高 | 重启 | root 凭据只用于初始化；日常使用 Gateway 专用数据库用户 |
| redis.* | .env 注入连接地址和密码，DB 等选项在 YAML | 高 | 重启 | 使用既有 Redis，不由 Gateway Compose 重建 |
| 本地上游地址与可选 Key | .env 提供，YAML 引用 | 高（Key） | 重启 | 管理台切换的活动上游会覆盖初始选择 |
| llama.request_timeout_seconds | LLAMA_REQUEST_TIMEOUT_SECONDS | 低 | 重启 | 0 不设 Gateway 整体时限；请求 Context 取消仍有效 |
| 模型加载与输出上限 | config.yaml | 低 | 重启 | 不替代 OpenResty 或调用方超时 |
| 模型目录与下载策略 | MODEL_HOST_DIR 在 .env；下载策略在 YAML | 低 | 重启 | 容器内模型目录固定为 /data/models |
| 日志与保留策略 | config.yaml | 高 | 重启 | 正文可能包含提示词/代码；retention_days=0 不自动清理 |

## 2. 远程 Provider（Web 管理台）

| 配置项 | 权威存储 | 编辑入口 | 生效时机 | 约束 |
| --- | --- | --- | --- | --- |
| ID、Base URL、默认模型、Key Header | MariaDB 中的远程 Provider 设置 | 远程 Provider 页面 | 保存后更新运行时 Registry | ID trim 后转小写，大小写不敏感唯一；内置 llamacpp/ollama ID 保留 |
| API Key | 加密后的数据库字段 | 新增/编辑/清除 Key 操作 | 保存后用于新请求 | AES-256-GCM；使用 LLM_GATEWAY_PROVIDER_ENCRYPTION_KEY；接口不返回明文或密文 |
| 模型映射、安全选项、响应体限制、请求时限 | MariaDB 中的远程 Provider 设置 | 远程 Provider 页面 | 保存后对新请求生效 | 默认执行 HTTPS 和私网目标保护；安全例外需显式开启 |
| 并发、队列及等待超时 | MariaDB 设置 + 当前进程的运行态 | 远程 Provider 页面 | 保存后动态更新共享许可器 | 队列为进程内控制；多副本时状态不是全局汇总 |
| Provider RPM、每日 Token 预算 | MariaDB 设置；计数使用 Redis | 远程 Provider 页面 | 保存后用于后续请求准入 | RPM=0、每日 Token 预算=0 表示关闭；每日预算按 UTC 日界重置 |

Provider ID 不是独立显示名，编辑时不可修改。前端在已加载列表中发现重复 ID 时会提示并禁用新增提交；后端会在锁内再次核验，返回 HTTP 409 和 provider_id_exists，且不会覆盖已有记录。ID 经过去首尾空白和小写规范化，所以大小写不同不构成不同 ID。

旧版文件预置的远程 Provider 不再受支持。config.yaml、config.example.yaml、主 .env 和 Compose 都不应再次加入 Provider 列表或 Provider Key 预置字段。历史数据库里曾存在的 source_override/deleted 标记只用于兼容读取，成功迁移后不再写入新的 Provider 记录。

## 3. 数据库运行配置

- 当前活动上游/模型选择保存在数据库；只有数据库中尚无活动上游时才使用 LLM_BACKEND。
- 每个上游的采样、思考与参数合并设置由管理台保存，不能在 .env 和 YAML 中维护第二份。
- 远程 Provider 存在 gateway_settings 的 remote_providers:web:v1 项中；Key 在写入数据库前加密。
- RPM 和每日 Token 预算使用 Redis 计数；并发和队列属于 Gateway 进程内状态。若部署多个副本，需要按这些边界理解限流和监控数据。

## 4. CI 契约检查

go run ./scripts/check_config_contract.go 检查：
1. 两份 YAML 是否只包含 Go 配置结构认可的字段。
2. 两份 YAML 的有效字段路径和静态值是否一致。
3. YAML 引用的变量是否在 .env.example 中声明且由 Compose 显式注入。
4. Compose 中没有默认值的插值变量是否在 .env.example 中声明。
5. YAML、环境示例和 Compose 是否重新引入远程 Provider 预置，以及配置说明文档是否仍包含旧的预置方式。

scripts/check_openapi_contracts.py 和 scripts/check_openapi_routes.py 检查关键 API Schema、Handler 状态和路由注册契约；Go CI 还包含单元/集成测试、竞态检查、静态检查、故障注入和 Docker 构建，Browser Smoke 验证管理台主要浏览器交互。

静态契约检查不访问生产数据库，不验证真实 API Key，也不证明供应商端点可用。真实上游端到端验收应在有授权且明确计费风险的环境中单独执行。

## 5. 变更流程

1. 先确定配置属于部署环境、静态 YAML 还是 Web/数据库运行时。
2. 新增环境变量时说明它只用于 Compose 插值，还是需要进入 Gateway 容器。
3. 同步模板、中文说明、契约检查和相关测试；不要通过放宽检查绕过配置差异。
4. 影响旧版本数据时说明兼容读取和迁移规则，并在生产升级前备份数据库。