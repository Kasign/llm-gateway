# LLM Gateway 配置清单与来源矩阵

> 新增配置时同步更新 Go 配置结构、规范模板、Compose 环境白名单（需要注入变量时）、中文部署说明和 CI 契约检查。本表记录稳定配置的主维护入口。

## 1. 服务静态配置

| 配置组 | 主维护入口 | 敏感性 | 生效时机 | 备注 |
| --- | --- | --- | --- | --- |
| server.host、server.port | config.yaml | 低 | 重启 | Compose 默认将 8088 绑定到宿主机回环地址 |
| server.trusted_proxies | config.yaml | 中 | 重启 | 仅填写真实代理来源 IP/CIDR |
| 管理 API Key | `.env` 提供值，YAML 负责引用 | 高 | 重启 | 与 Web 管理会话和调用方 `sk-llm-*` Key 分离 |
| Web 控制台初始密码哈希与保护默认值 | 主 `.env` 的 `LLM_GATEWAY_ADMIN_PASSWORD_HASH`、`LLM_GATEWAY_WEB_PASSWORD_ENABLED` 由 Compose 显式注入 | 高 | 重启；控制台保存后以数据库为准 | 新哈希格式为 `pbkdf2:`，兼容旧 `pbkdf2$`；`gateway_settings.web_admin_login` 保存后优先。忘记密码时用 `LLM_GATEWAY_WEB_LOGIN_RESET_FROM_ENV=true` 临时恢复，改密后撤销 |

> 新增配置时同步更新 Go 配置结构、规范模板、Compose 环境白名单（需要注入变量时）、中文部署说明和 CI 契约检查。本表记录稳定配置的主维护入口。

## 1. 服务静态配置

| 配置组 | 主维护入口 | 敏感性 | 生效时机 | 备注 |
| --- | --- | --- | --- | --- |
| server.host、server.port | config.yaml | 低 | 重启 | Compose 默认将 8088 绑定到宿主机回环地址 |
| server.trusted_proxies | config.yaml | 中 | 重启 | 仅填写真实代理来源 IP/CIDR |
| 管理 API Key | `.env` 提供值，YAML 负责引用 | 高 | 重启 | 与 Web 管理会话和调用方 `sk-llm-*` Key 分离 |
；`gateway_settings.web_admin_login` 保存后优先 |
| database.* | 容器地址由 LLM_GATEWAY_DB_HOST 注入；其余非敏感设置在 YAML，密码由 .env 注入 | 高 | 重启 | root 凭据用于自动初始化，日常使用 Gateway 专用数据库账号 |
| redis.* | 地址由 LLM_GATEWAY_REDIS_ADDR 注入；DB 与密码在 YAML/.env 分层配置 | 高 | 重启 | 使用既有 Redis，不由 Gateway Compose 重建 |
| 本地后端类型、Base URL、上游 Key | .env 提供值，YAML 负责引用 | 高（Key） | 重启；活动选择可被数据库覆盖 | LLM_BACKEND 仅作为数据库无活动选择时的初始默认 |
| llama.request_timeout_seconds | LLAMA_REQUEST_TIMEOUT_SECONDS 经 Compose 注入 | 低 | 重启 | 0 不设 Gateway 整体时限；调用方取消仍然有效 |
| 模型加载与输出上限 | config.yaml | 低 | 重启 | 不代替 OpenResty/客户端超时 |
| 模型目录与下载 | MODEL_HOST_DIR 在 .env；下载策略在 YAML | 低 | 重启 | 容器内模型目录固定为 /data/models |
| 日志与保留策略 | config.yaml | 高（可能含提示词/代码） | 重启 | retention_days=0 默认不主动清理；payload 留存需评估隐私与磁盘 |

## 2. 远程 Provider

| 来源 | 管理内容 | Web 是否可编辑 | 生效时机 | 冲突规则 |
| --- | --- | --- | --- | --- |
| YAML: providers.openai_compatible[] | ID、URL、Key 引用、默认模型、模型映射、安全选项、并发、等待队列、RPM、每日 Token 预算和超时 | 否，部署托管项只读 | 重启 | 不得与环境预置或 Web Provider 重复 ID |
| 环境变量: LLM_GATEWAY_PROVIDER_<ID>_* | Key、模型映射、并发/队列/RPM、每日 Token 预算、响应体限制、请求时限和网络安全选项 | 否，部署托管项只读 | 重启 | DeepSeek/OpenAI 通过显式环境白名单；自定义 ID 使用 providers.env |
| Web / 数据库 | 新建、更新、启用/停用、删除、模型映射、并发/队列策略和凭据轮换 | 是 | 按后端持久化和 Registry 热更新契约生效 | Key 加密保存且不回显；活动 Provider 受保护 |

Provider 环境字段以 internal/config/provider_env.go 中的解析器清单为准。CI 检查它与 .env.example、providers.env.example 及 Compose 中的 DeepSeek/OpenAI 字段一致。

部署托管 Provider 在启动注册时会同时应用 `max_concurrent_requests`、`max_queue_size`、`queue_wait_timeout_seconds` 与 `request_timeout_seconds`；Web/数据库 Provider 则在保存时通过共享许可器热更新并发和排队容量。配置文件中写入这些字段不等于供应商连通性已验证。

`token_budget_per_day=0` 默认关闭；正数按 Provider ID 使用 Redis UTC 自然日预算，在所有 API Key 和 Agent 间共享。准入时原子预占请求体字节估算与强制输出上限，余额不足时尽量收紧单次输出上限；收到可信供应商 usage 后结算为真实用量。没有可信 usage 时保守保留预占至日桶过期，因此这是预算保护，不是各家计费规则的精确复刻。


Provider 等待队列是**进程内**准入控制，不依赖 Redis：`max_queue_size=0` 默认关闭并保持旧的立即拒绝行为；启用后采用有界 FIFO（1–1024），请求取消会从队列移除。等待超时默认 30 秒，最大 3600 秒；队列满或等待超时返回 503（`provider_queue_full` / `provider_queue_timeout`）。已经提交 HTTP 状态行的流式响应以安全 SSE error 结束。管理台显示当前排队数与队列超时累计数；多副本部署时，这些指标不是全局合计。

## 3. 数据库运行配置

- 当前活动 Provider/模型选择在数据库中；首次初始化时才使用 LLM_BACKEND。
- 每个上游的采样、思考和参数合并策略通过管理台保存，不能在 .env 与 YAML 中维护第二份。
- Web Provider 与部署预置 Provider 有不同的生命周期；状态更新必须遵守 Provider ID 冲突、Registry 原子切换和并发预算回滚约束。

## 4. CI 契约检查

go run ./scripts/check_config_contract.go 会检查：
1. 两份 YAML 是否包含 Go 配置结构认可的字段。
2. 两份 YAML 的有效字段路径和静态值是否完全一致；部署差异统一走环境变量，不允许维护例外值。
3. YAML 变量是否同时在 .env.example 声明并由 Compose 显式注入。
4. Compose 中没有默认值的插值变量是否在 .env.example 声明。
5. Provider 解析器字段是否出现在两个环境示例和 DeepSeek/OpenAI Compose 白名单。

这是静态契约门禁，不会访问生产数据库、验证真实 API Key 或证明供应商可用。慢模型超时、Provider 冲突、Web 更新/回滚和部署兼容性仍需统一回归。

## 5. 变更流程

1. 先确认字段属于部署环境、YAML、Provider 预置还是 Web/数据库。
2. 新环境变量必须说明只用于 Compose 主机插值，还是需要进入 Gateway 容器；后者必须加入显式白名单或 providers.env。
3. 同步模板、文档和代码默认值；不以放宽 CI 检查替代修复差异。
4. 可能改变旧部署行为时，先提供兼容期和迁移说明，不能无提示切换超时或日志留存。
