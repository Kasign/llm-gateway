# FREE-02 官方免费计划调研

> 状态：已完成（官方政策比较完成；首个 Provider 保留条件性建议，尚未创建账户、配置 Key 或进行真实请求）
> 核验日期：2026-11-23
> 仓库：Kasign/llm-gateway
> 分支：dev
> 前序任务：[FREE-01 现有 Provider 接入能力盘点](FREE-01-现有Provider接入能力盘点.md)
> 当前结论：优先评估 Google AI Studio 与 Groq；OpenRouter 作为聚合型备选；Cloudflare Workers AI 作为独立计量模型备选；Cerebras 与 Hugging Face 不列为持续免费 API。

## 1. 调研口径

本次只核验厂商/平台官方文档、价格页、控制台政策和服务条款。第三方整理页不作为额度或地区政策的事实来源。

“免费”严格区分：

- **持续免费层/持续免费额度**：有明确、周期性刷新或持续有效的免费额度。
- **免费模型变体**：只对特定模型 ID 免费，且可能受平台级请求上限约束。
- **试用额度**：一次性/时间限定，不能视为长期免费。
- **付费订阅附赠额度**：用户必须先购买订阅，不属于免费 API。
- **免费网页体验**：不能等同于免费 API。

所有额度都必须绑定到具体账户、模型、项目、周期和官方核验时间。未知值保持未知，不填 0，不把不同厂商的请求数、Token、Neurons 直接相加。

## 2. 候选横向比较

| 候选 | 免费类型 | 官方免费额度/限制 | 付款条件 | API 与当前 Adapter 适配 | 初步判断 |
|---|---|---|---|---|---|
| Google AI Studio / Gemini API | 持续免费层，限部分模型 | 免费输入/输出 Token；RPM、TPM、RPD 按项目和模型动态变化，实际额度以 AI Studio Rate Limit 页面为准 | Free Tier 使用活跃 Project；部分导入项目需验证。升级 Paid 需关联 Billing，至少预付 5 美元 | OpenAI-compatible Chat Completions、Streaming、Function Calling、Embeddings、Models List；但官方 Base URL 路径为 `/v1beta/openai/`，当前 Adapter 的路径拼接规则不能直接正确构造该路径 | 免费层和地区覆盖有吸引力；数据使用与路径兼容是主要风险 |
| GroqCloud | 持续 Free Plan | 按模型分别限制 RPM/RPD/TPM/TPD；例如 GPT-OSS 120B、20B 与 Qwen 3.8 27B：30 RPM、1K RPD、8K TPM、200K TPD | 官方说明升级 Developer Tier 需要有效付款方式；Free Tier 是否要求绑卡，公开文档未明确 | OpenAI-compatible；`https://api.groq.com/openai/v1`、Bearer Key、Chat Completions、Streaming、Responses、Models List；Base URL 以 `/v1` 结尾，适配现有路径规则 | 现有 Adapter 适配度高、额度可读；服务条款对 Consumer Use 和转售有限制，需先确认使用边界 |
| Cerebras Inference | 仅限时 Free Trial，不是持续免费层 | 新账户在添加已验证付款方式后获得 5 美元试用额度，30 天后过期；耗尽或过期后 API/Playground 停止，购买额度后恢复 | 必须添加已验证付款方式才能激活试用 | OpenAI-compatible Chat Completions，`https://api.cerebras.ai/v1`，Bearer Key | 不满足长期免费目标，排除首批持续免费候选 |
| Cloudflare Workers AI | 持续每日免费额度 | Workers Free 每日 10,000 Neurons，UTC 00:00 重置；文本生成默认 300 RPM。部分模型要求 Workers Paid；这类模型在 Free 计划下返回 403 / 内部码 5035 | 免费计划包含免费额度；部分模型要求付费账单方式或 Workers Paid | OpenAI-compatible Chat Completions、Embeddings；Responses 仅 GPT-OSS 模型且仅非流式。Base URL 以 `/v1` 结尾可复用 Adapter；模型列表走独立 Account API，不是 `/v1/models` | 免费额度持续，但按模型计算 Neurons，模型可用性和模型目录探测需要专门处理 |
| OpenRouter | 持续免费模型变体 | Free 计划列出 25+ 免费模型、4 个免费 Provider；`:free` 模型 20 RPM、50 RPD；累计购买 10 美元 Credits 后提升至 1,000 RPD，仍为 20 RPM。失败请求也计入 RPD | `:free` 变体无需购买 Credits；其他标价 0 美元的模型可能仍要求账户有充值余额 | OpenAI-compatible Chat Completions；`openrouter/free` 可动态选取免费模型；`/api/v1/models` 可用于模型发现；Base URL 以 `/v1` 结尾 | 最适合快速获得多家免费模型，但每日额度较低、模型变化快、条款禁止转售/开发竞争服务 |
| Hugging Face Inference Providers | Free 用户没有 API 月度额度 | Free Users 月度 Credits 为 0；PRO 用户每月 2 美元通用计算 Credits，Team/Enterprise 每席位 2 美元 | Free 用户需购买 Credits 或升级 PRO；PRO 本身是付费订阅 | 统一推理入口，支持多种模型/Provider；但没有免费 API Credits | 不属于免费 API 候选，排除；不能把免费网页 Widget 当作 API 免费额度 |

## 3. 逐家官方证据

### 3.1 Google AI Studio / Gemini API

**免费计划**

- 官方价格页明确提供 Free Tier：只开放部分模型，输入和输出 Token 免费。
- Free Tier 的提示词/输出可能用于改进 Google 产品；Paid Tier 明确不用于该目的。
- 新用户在 AI Studio 接受条款后会自动创建默认 Project 和 API Key；Free Tier 要求活跃 Project，部分导入的 Google Cloud Project 需要验证。
- Google AI Studio / Gemini API 的可用地区列表明确包含 Singapore。
- 升级 Paid Tier 要关联 Billing，并至少预付 5 美元；这不是 Free Tier 的要求。

**额度与错误**

- 限额按 Project 而不是 API Key 计算，通常包含 RPM、输入 TPM、RPD；部分模型还有其他指标。
- RPD 在 Pacific Time 午夜重置。
- 不同模型的 Free Tier 可用性不同；官方示例中部分模型在 Free Tier 明确不支持。
- 精确实时额度在 AI Studio Rate Limit 页面查看，官方说明额度不保证、实际容量可能变化。
- 超过速率限制返回 429；Free Tier 没有 spend-based rate limit，但模型/项目速率限制仍存在。

**协议与适配**

- 官方 OpenAI Compatibility 文档给出 Chat Completions、Streaming、Function Calling、Embeddings 和 Models List。
- 官方 Base URL：`https://generativelanguage.googleapis.com/v1beta/openai/`。
- 官方 REST 示例使用 `Authorization: Bearer GEMINI_API_KEY`。
- 官方模型列表路径为 `/v1beta/openai/models`。
- 当前 Gateway Adapter 对 Base URL 路径前缀的处理要求末尾为 `/v1` 时才会剥离请求路径中的 `/v1`；Gemini Base URL 末尾为 `/openai`，因此现有路径拼接会多出一段 `/v1`。**不能仅在管理台填入 Gemini 官方 Base URL 就认定可用**，需要后续修复通用路径前缀处理或提供明确的路径映射配置。
- 官方文档将 OpenAI compatibility 标注为 Beta，实际参数支持仍需按模型验证。

**隐私和地区风险**

- Free Tier 数据可能用于改进 Google 产品，不适合默认发送敏感业务数据。
- Singapore 在官方可用地区列表中；其他国家/地区必须以官方列表为准。
- Free Tier 的实际模型和速率限制以 Project 的 AI Studio 控制台为准，不能在静态目录中写死为所有用户通用的固定数值。

官方来源：

- [Gemini API Pricing](https://ai.google.dev/gemini-api/docs/pricing)
- [Gemini API Billing](https://ai.google.dev/gemini-api/docs/billing)
- [Gemini API Rate Limits](https://ai.google.dev/gemini-api/docs/rate-limits)
- [Gemini API Available Regions](https://ai.google.dev/gemini-api/docs/available-regions)
- [Gemini OpenAI Compatibility](https://ai.google.dev/gemini-api/docs/openai)
- [Gemini API Keys](https://ai.google.dev/gemini-api/docs/api-key)

### 3.2 GroqCloud

**免费计划与限额**

- 官方 Rate Limits 页面区分 Free Plan Limits 和 Developer Plan Limits。
- Free Plan 限额按模型变化，不是全账户统一的一组数字。
- 例如官方 Free Plan 表中的 `openai/gpt-oss-120b`、`openai/gpt-oss-20b` 和 `qwen/qwen3.8-27b` 均列出 30 RPM、1K RPD、8K TPM、200K TPD。
- `canopylabs/orpheus` 两个模型示例为 10 RPM、100 RPD、1.2K TPM、3.6K TPD，说明不能把单一模型额度复制给整个 Provider。
- 额度按 Organization 计算；429 表示超出限额。达到限制时官方会返回 `Retry-After`，并提供 `x-ratelimit-limit-*`、`remaining-*`、`reset-*` 等响应头。
- Developer Tier 需要有效付款方式；官方 Billing FAQ 未在本次可见文档中明确 Free Tier 是否需要绑卡，因此该项标记为“未确认”，不作无卡承诺。

**协议与模型**

- 官方 Quickstart 的 Base URL 为 `https://api.groq.com/openai/v1`，Chat Completions 使用 Bearer Key。
- 官方文档提供 Models API `GET /openai/v1/models`。
- 官方 API Reference 覆盖 Chat Completions 和 Responses API；官方还提供 Tool Use、Structured Outputs、Streaming 等文档。
- 现有 Gateway Adapter 的 Base URL 路径规则与 Groq 的 `/openai/v1` 结构匹配，模型列表探测路径也匹配。

**地区与条款**

- Groq Trust Center 当前列出的数据中心在美国、澳大利亚、加拿大、芬兰和沙特阿拉伯；区域 API Endpoint 的选择说明仅针对 Enterprise 客户。普通 Free 用户不能据此获得指定区域处理保证。
- Groq Services Agreement 写明 Cloud Services / AI Model Services 不面向 Consumer Use，并限制未经批准的转售、再许可、转移或分发；这是否允许个人开发者自用的私有 Gateway，需要结合实际账号协议确认。
- 服务协议说明 Groq 不会将输入/输出用于训练，除非客户明确授权；仍需以实际适用的服务协议和账户设置为准。

官方来源：

- [Groq Rate Limits](https://console.groq.com/docs/rate-limits)
- [Groq Supported Models](https://console.groq.com/docs/models)
- [Groq Quickstart](https://console.groq.com/docs/quickstart)
- [Groq Billing FAQs](https://console.groq.com/docs/billing-faqs)
- [Groq Services Agreement](https://console.groq.com/docs/legal/services-agreement)
- [Groq Trust Center FAQ](https://trust.groq.com/faq)

### 3.3 Cerebras Inference

**免费性质**

- 官方 Rate Limits FAQ 明确回答：没有永久免费 Tier。
- 新账户添加已验证付款方式后获得 5 美元 Free Trial Credits，有效期 30 天。
- 不添加已验证付款方式时，Playground 和 API 不会激活。
- Credits 用完或过期后，API 和 Playground 会停止，购买 Pay as You Go Credits 后才恢复。

**试用额度期间的模型限额**

- `gpt-oss-120b`：5 RPM、30K Uncached TPM、90K Total TPM、1M TPH、1M TPD。
- `qwen-3.8-27b`：300 RPM、150K Uncached TPM、750K Total TPM；该模型没有小时/日 Token 上限。官方注明 Total TPM 的 750K 为临时调整值。
- 超过速率限制返回 429；模型级限额和账户实际限额以控制台 Limits 为准。

**协议**

- 官方 Quickstart 使用 `https://api.cerebras.ai/v1/chat/completions` 和 Bearer Key。
- 标准 Chat Completions 可复用现有 Adapter。

官方来源：

- [Cerebras Rate Limits](https://inference-docs.cerebras.ai/support/rate-limits)
- [Cerebras Quickstart](https://inference-docs.cerebras.ai/quickstart)
- [Cerebras Inference Pricing](https://www.cerebras.ai/pricing)

### 3.4 Cloudflare Workers AI

**免费计划与额度**

- Workers AI 同时包含在 Workers Free 和 Workers Paid 计划中。
- Workers Free 每天有 10,000 Neurons 免费额度，UTC 00:00 重置。
- 超出免费计划额度需要升级 Workers Paid；Paid 计划超出每日 10,000 Neurons 后按 0.011 美元/1,000 Neurons 计费。
- Neurons 是计算量单位，不等于 Token；每个模型的输入/输出 Neurons 消耗不同，不能把 10,000 Neurons 直接写成固定 Token 数。
- 官方明确列出部分模型要求 Paid Billing Method / Workers Paid。Free 计划请求这些模型会返回 403，内部错误码 5035，提示升级。
- Text Generation 默认 300 RPM；要求 Paid 的模型，Standard Workers AI Billing 为 20 RPM，使用预付 AI Gateway Credits 为 50 RPM。

**协议与模型目录**

- 官方 OpenAI-compatible REST Base URL：`https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1`。
- 支持 Chat Completions 和 Embeddings；Responses API 仅支持 GPT-OSS 模型，并且只允许 `stream: false`。
- 模型目录使用独立 Account API：`GET /accounts/{account_id}/ai/models/search`，不是 OpenAI 的 `/v1/models`。
- 因此现有 Gateway 的通用推理转发路径可以复用，但当前 `GET /v1/models` 连接测试不匹配，需要模型目录探测策略单独适配；不能用连接测试失败推断推理端点不可用。
- 模型是否需要付费计划必须按具体 Model ID 核验，不能只在 Provider 级标记“免费”。

官方来源：

- [Workers AI Pricing](https://developers.cloudflare.com/workers-ai/platform/pricing/)
- [Workers AI Limits](https://developers.cloudflare.com/workers-ai/platform/limits/)
- [OpenAI Compatible API Endpoints](https://developers.cloudflare.com/workers-ai/configuration/open-ai-compatibility/)
- [Cloudflare AI Model Search API](https://developers.cloudflare.com/api/resources/ai/subresources/models/methods/list/)
- [Models requiring Workers Paid](https://developers.cloudflare.com/changelog/post/2026-07-28-models-require-workers-paid/)

### 3.5 OpenRouter

**免费计划与模型**

- 官方 Pricing 页的 Free 计划列出 25+ Free Models、4 Free Providers、API Access。
- ID 以 `:free` 结尾的免费变体无需购买 Credits；其他价格为 0 美元的模型可能仍要求账户已有充值余额，不能只按价格字段判断是否免费。
- Free variants 的统一平台上限为 20 RPM、50 RPD；累计购买 10 美元 Credits 后提升到 1,000 RPD，仍为 20 RPM。失败请求也计入每日上限。
- `openrouter/free` 会在符合当前请求能力条件的免费模型中动态选择；模型目录和可用模型会变化。若需要稳定模型身份，应使用具体的 `:free` Model ID，而不是把动态 Router 当成固定模型。
- Free Models 页面按近 7 天使用量更新排名，不是 SLA 或长期可用承诺。

**协议与额度查询**

- Chat Models 使用 OpenAI-compatible Chat Completions。
- API Key 元数据端点 `GET /api/v1/key` 可返回 `is_free_tier`、`free_model_daily_requests.used/limit`、`limit_remaining` 等字段。
- Free tier rate limit / upstream provider limit 都可能产生 429；Credits、Key Limit 或 In-flight Budget 问题会出现 402。错误的 `metadata.limit_source` 用于区分具体限额来源。
- 当前 Gateway Base URL `https://openrouter.ai/api/v1` 与现有 Adapter 路径规则匹配；模型列表和 Key 状态接口都可在同一 `/api/v1` 前缀下访问。

**地区、隐私与条款**

- OpenRouter 的模型供应方可能按国家/地区限制访问某些模型；用户不得通过 VPN/代理规避限制。
- Free 计划不提供 Business/Enterprise 的 EU/US In-region Routing。
- 输入会传递给实际模型 Provider；不同模型 Provider 的留存、训练和数据处理政策不同。OpenRouter 会在模型页面提供可见信息，但用户仍需逐模型审查。
- OpenRouter Terms 禁止转售模型 API Access 或开发竞争服务；如果未来将 Gateway 作为公开模型聚合/转售服务，不能默认认为符合条款。私有自用与公开服务必须区分。
- 官方 Terms 还允许模型列表随时新增/移除，因此免费模型目录必须做有效期和变更治理。

官方来源：

- [OpenRouter Free Models](https://openrouter.ai/collections/free-models)
- [OpenRouter Pricing](https://openrouter.ai/pricing)
- [OpenRouter Limits](https://openrouter.ai/docs/api-reference/limits)
- [OpenRouter Terms of Service](https://openrouter.ai/terms)
- [OpenRouter Privacy Policy](https://openrouter.ai/privacy)

### 3.6 Hugging Face Inference Providers

- 官方 Pricing and Billing 明确：Free Users 的月度 Credits 为 0；Free 用户若要使用 Inference Providers，需要购买 Credits 或升级 PRO。
- PRO 用户每月包含 2 美元通用计算 Credits；Team / Enterprise 每席位每月 2 美元。这是付费订阅附赠额度，不是免费账户 API 额度。
- Inference Providers 提供统一模型入口，Chat/Vision/Agent 请求由 OpenRouter 路由至实际 Provider；模型价格按 Provider 计费，不能把 Hugging Face 的免费网页 Widget 或开放模型权重等同于免费 API。
- 结论：从“持续免费 API”候选中排除；如后续考虑低成本付费聚合，可单独重新评估。

官方来源：

- [Hugging Face Inference Providers Pricing](https://huggingface.co/docs/inference-providers/main/pricing)
- [Hugging Face Inference Providers](https://huggingface.co/docs/inference-providers/index)
- [Hugging Face Providers](https://huggingface.co/docs/inference-providers/providers)

## 4. 关键对比结论

### 4.1 持续免费候选

按本次官方证据：

- **Google AI Studio**：有持续免费层；免费模型范围有限，项目级限额动态变化；Singapore 在可用地区；Free Tier 数据用于改进 Google 产品；当前 Adapter 的 Base URL 路径规则需要修正。
- **Groq**：有持续 Free Plan；部分模型 1,000 RPD、30 RPM；模型限额清晰且有响应头；现有 Adapter 适配度高；但 Consumer Use、区域处理和转售边界必须先确认。
- **Cloudflare Workers AI**：有持续 10,000 Neurons/日额度；模型计量和付费模型限制需要模型级元数据；现有 Adapter 可转发 Chat，但模型目录/连接测试不兼容。
- **OpenRouter**：有持续免费模型变体；20 RPM、50 RPD；可通过单一 Provider 获得多家模型；但每日请求数较低、上游数据政策不同、免费模型可用性变化且条款限制公开转售/竞争服务。

### 4.2 不作为持续免费 API

- Cerebras：5 美元、30 天、需验证付款方式的试用，不是永久免费。
- Hugging Face：Free 用户没有 Inference Providers 月度 Credits；PRO 附赠额度属于付费订阅权益。

## 5. 首个 Provider 建议

### 5.1 首选候选：Google AI Studio（有条件）

优点：

- 持续免费层，不是一次性试用；
- 官方地区列表包含 Singapore；
- 免费输入/输出 Token；
- OpenAI-compatible Chat Completions、Streaming、Function Calling、Embeddings 和 Models List；
- 官方 API Key 直接使用 Bearer 鉴权。

前置条件：

1. 修复当前 Adapter 对 `/v1beta/openai` 这类非 `/v1` 结尾 Base URL 前缀的路径拼接限制，不能通过错误拼接或双重路径绕过。
2. 用户必须接受 Free Tier 输入/输出可能用于改进 Google 产品；敏感内容不应通过 Free Tier 发送。
3. 按 Project / Model 读取实际 Free Tier 额度，不在 Provider 默认配置中写死 RPM/TPM/RPD。

### 5.2 第二候选：GroqCloud

如果 Groq 明确确认开发者个人自用符合其 Services Agreement，则 Groq 是当前代码改动最少的接入对象：

- Base URL、Bearer 鉴权、`/v1/models` 和 Chat Completions 都匹配现有 Adapter；
- Free Plan 额度按模型公开，部分模型 1K RPD；
- 响应头提供剩余/重置限额信息。

但 Groq Services Agreement 对 Consumer Use 的限制必须先澄清；在未澄清前，不把它标记为已批准的首个生产 Provider。

### 5.3 其他候选的定位

- **OpenRouter**：适合快速验证“一个 Provider 暴露多家免费模型”的聚合模式；但 50 RPD 对高频个人使用偏低，且公开转售/竞争服务限制严格。只作为私有自用候选，不作为未来公开聚合服务的默认上游。
- **Cloudflare Workers AI**：适合后续接入多模态/本地模型之外的免费推理资源；但需增加独立模型目录探测，并处理 Neurons 与模型级 Paid 要求。
- **Cerebras**：仅在接受绑卡和 30 天试用时作为短期测速候选，不进入长期免费清单。
- **Hugging Face**：不属于免费 API 候选；只有付费订阅/购买 Credits 后才纳入低成本方案比较。

**本任务不直接创建或配置任何 Provider。** 最终首个 Provider 应在 FREE-03 元数据契约完成后，结合账号实际资格、数据隐私选择和一次真实短请求验收确定。

## 6. 对后续任务的约束

1. FREE-03 必须支持不同免费类型：持续免费层、每日免费额度、免费模型变体、限时试用、付费订阅附赠 Credits、已失效。
2. 额度计量单位必须原样保存：Token、请求次数、Neurons、美元 Credits 不能归一成一个无单位的数字。
3. 限额必须能绑定 Provider、Model、Account/Project/Organization 和周期；模型级限额不得提升为 Provider 全局额度。
4. 必须记录是否需要绑卡/验证、地区限制、数据训练/留存策略、官方条款和模型条款链接。
5. 官方限额可动态变化的 Provider 应记录“以账户控制台为准”，不将文档示例固化成全体用户的保证额度。
6. 错误状态必须区分 402 余额/预算问题、403 模型/计划无权限、429 速率或容量限制；不能把所有 429 当成免费额度耗尽。
7. FREE-06 只对最终选定 Provider 做受控真实短请求，不在本任务中申请或存储 API Key。

## 7. 验收记录

- 候选范围：Google AI Studio、GroqCloud、Cerebras Inference、Cloudflare Workers AI、OpenRouter、Hugging Face Inference Providers。
- 官方来源核验日期：2026-11-23。
- 结果：六家候选均已分类；两家持续免费 API 以外的试用/付费附赠额度已排除；四家持续免费候选已记录额度、协议、条款和适配风险。
- 代码变更：无。FREE-02 是官方政策与候选评估任务，不应为了盘点而提前实现 Provider。
- 真实 API 验收：未执行；没有创建账号、填写 Key 或发起真实请求。
- 后续任务：FREE-03 免费政策元数据结构设计。
