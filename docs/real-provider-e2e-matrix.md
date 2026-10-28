# 真实 Provider 端到端验收矩阵

> 当前状态：测试代码与安全执行步骤已准备；真实端点执行状态为“未测”，需在有授权、计费额度明确的环境中单独运行。默认 CI 不会发出真实上游请求。

## 通用安全规则

- 先确认供应商、端点、模型 ID、账号权限与可能产生的费用；每个测试最多请求一条极短回复，不把生产用户数据用于探测。
- 仅使用 HTTPS Base URL，不包含用户名密码、Query 或 Fragment；不要把 Key 写入仓库、文档、CI 变量截图或命令参数。
- 使用 `read -s` 从交互终端读取临时 Key；测试代码不会打印 Key 或原始上游错误正文。结束后执行 `unset` 清理环境变量。
- 一次只运行一个指定的 E2E 测试，不使用 `go test ./...` 触发不相关测试，也不要把测试开关长期保留在 shell profile 或部署环境。
- 成功标准仅代表这一个 Provider、模型和协议探测通过；不代表流式、工具调用、视觉、Reasoning、限流、计费或其它模型均已兼容。

## 测试矩阵

| 接口/适配器 | 单独启用变量 | 必需输入变量 | 成功判定 | 当前状态 |
|---|---|---|---|---|
| OpenAI-compatible Chat Completions | `LLM_GATEWAY_PROVIDER_E2E=1` | `LLM_GATEWAY_E2E_BASE_URL`、`LLM_GATEWAY_E2E_API_KEY`、`LLM_GATEWAY_E2E_MODEL` | HTTP 200、JSON 可解析、至少一个 choice | 未测 |
| OpenAI-compatible Responses | `LLM_GATEWAY_RESPONSES_E2E=1` | 同上 | HTTP 200、`object=response`、`status=completed`、含 output | 未测 |
| Anthropic Messages | `LLM_GATEWAY_ANTHROPIC_E2E=1` | `LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL`、`LLM_GATEWAY_ANTHROPIC_E2E_API_KEY`、`LLM_GATEWAY_ANTHROPIC_E2E_MODEL` | HTTP 200、`type=message`、`role=assistant`、包含 content block | 未测 |

## OpenAI-compatible Chat Completions

在仓库根目录运行。按目标供应商替换 HTTPS Base URL 与模型 ID；不要把 API Key 写进下面的命令行字面量。

```bash
export LLM_GATEWAY_E2E_BASE_URL='https://api.example.com/v1'
export LLM_GATEWAY_E2E_MODEL='your-model-id'
read -r -s -p 'Provider API Key: ' LLM_GATEWAY_E2E_API_KEY; printf '\n'
export LLM_GATEWAY_E2E_API_KEY
# 如果供应商要求非 Authorization 头，再设置 LLM_GATEWAY_E2E_API_KEY_HEADER，例如 x-api-key
export LLM_GATEWAY_PROVIDER_E2E=1
go test ./internal/provider -run '^TestOpenAICompatibleProviderRealEndpointE2E$' -count=1
unset LLM_GATEWAY_PROVIDER_E2E LLM_GATEWAY_E2E_API_KEY LLM_GATEWAY_E2E_MODEL LLM_GATEWAY_E2E_BASE_URL LLM_GATEWAY_E2E_API_KEY_HEADER
```

## OpenAI-compatible Responses

沿用上面的 `LLM_GATEWAY_E2E_BASE_URL`、`LLM_GATEWAY_E2E_API_KEY`、`LLM_GATEWAY_E2E_MODEL` 临时环境变量，单独执行：

```bash
export LLM_GATEWAY_RESPONSES_E2E=1
go test ./internal/provider -run '^TestOpenAIResponsesRealEndpointE2E$' -count=1
unset LLM_GATEWAY_RESPONSES_E2E
```

执行完也要清理上一步的 API Key 和其它输入变量。

## Anthropic Messages

```bash
export LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL='https://api.anthropic.com/v1'
export LLM_GATEWAY_ANTHROPIC_E2E_MODEL='your-model-id'
read -r -s -p 'Anthropic API Key: ' LLM_GATEWAY_ANTHROPIC_E2E_API_KEY; printf '\n'
export LLM_GATEWAY_ANTHROPIC_E2E_API_KEY
export LLM_GATEWAY_ANTHROPIC_E2E=1
go test ./internal/provider -run '^TestAnthropicMessagesRealEndpointE2E$' -count=1
unset LLM_GATEWAY_ANTHROPIC_E2E LLM_GATEWAY_ANTHROPIC_E2E_API_KEY LLM_GATEWAY_ANTHROPIC_E2E_MODEL LLM_GATEWAY_ANTHROPIC_E2E_BASE_URL
```

## 验收记录

| 日期 | Provider / 模型 | 协议测试 | 结果（通过/失败/未测） | 脱敏证据 |
|---|---|---|---|---|
| 待执行 | 待填写 | Chat / Responses / Messages | 未测 | 不含 Key、Prompt 和原始错误正文 |

额外兼容能力（SSE、工具调用、JSON/结构化输出、Reasoning、图像、429/限流、余额不足、Usage/账单核对）必须按实际使用需求单独列项；上表的简短非流式探测不会覆盖这些行为。
