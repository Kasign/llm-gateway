# LLM Gateway 配置清单与来源矩阵

> 新增配置时同步更新 Go 配置结构、规范模板、Compose 环境白名单（需要注入变量时）、中文部署说明和 CI 契约检查。本表记录稳定配置的主维护入口。

## 1. 服务静态配置

| 配置组 | 主维护入口 | 敏感性 | 生效时机 | 备注 |
| --- | --- | --- | --- | --- |
| server.host、server.port | deploy/config.yaml | 低 | 重启 | Compose 默认将 8088 绑定到宿主机回环地址 |
| server.trusted_proxies | deploy/config.yaml | 中 | 重启 | 仅填写真实代理来源 IP/CIDR |
| 管理认证和密码哈希 | deploy/.env 提供值，YAML 负责引用 | 高 | 重启 | 管理 API Key、Web 登录与调用方 sk-llm-* Key 是不同凭据域 |
| database.* | 容器地址由 LLM_GATEWAY_DB_HOST 注入；其余非敏感设置在 YAML，密码由 .env 注入 | 高 | 重启 | root 凭据用于自动初始化，日常使用 Gateway 专用数据库账号 |
| redis.* | 地址由 LLM_GATEWAY_REDIS_ADDR 注入；DB 与密码在 YAML/.env 分层配置 | 高 | 重启 | 使用既有 Redis，不由 Gateway Compose 重建 |
| 本地后端类型、Base URL、上游 Key | .env 提供值，YAML 负责引用 | 高（Key） | 重启；活动选择可被数据库覆盖 | LLM_BACKEND 仅作为数据库无活动选择时的初始默认 |
| llama.request_timeout_seconds | LLAMA_REQUEST_TIMEOUT_SECONDS 经 Compose 注入 | 低 | 重启 | 0 不设 Gateway 整体时限；调用方取消仍然有效 |
| 模型加载与输出上限 | deploy/config.yaml | 低 | 重启 | 不代替 OpenResty/客户端超时 |
| 模型目录与下载 | MODEL_HOST_DIR 在 .env；下载策略在 YAML | 低 | 重启 | 容器内模型目录固定为 /data/models |
| 日志与保留策略 | deploy/config.yaml | 高（可能含提示词/代码） | 重启 | retention_days=0 默认不主动清理；payload 留存需评估隐私与磁盘 |

## 2. 远程 Provider

| 来源 | 管理内容 | Web 是否可编辑 | 生效时机 | 冲突规则 |
| --- | --- | --- | --- | --- |
| YAML: providers.openai_compatible[] | ID、URL、Key 引用、默认模型、模型映射、安全选项、限流和超时 | 否，部署托管项只读 | 重启 | 不得与环境预置或 Web Provider 重复 ID |
| 环境变量: LLM_GATEWAY_PROVIDER_<ID>_* | Key、模型映射、并发/RPM、响应体限制、请求时限和网络安全选项 | 否，部署托管项只读 | 重启 | DeepSeek/OpenAI 通过显式环境白名单；自定义 ID 使用 providers.env |
| Web / 数据库 | 新建、更新、启用/停用、删除、模型映射和凭据轮换 | 是 | 按后端持久化和 Registry 热更新契约生效 | Key 加密保存且不回显；活动 Provider 受保护 |

Provider 环境字段以 internal/config/provider_env.go 中的解析器清单为准。CI 检查它与 .env.example、providers.env.example 及 Compose 中的 DeepSeek/OpenAI 字段一致。

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
