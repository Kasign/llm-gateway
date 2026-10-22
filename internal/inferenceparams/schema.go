// inferenceparams 定义网关侧默认可配置项（schema）与存储结构；与 proxy 合并逻辑解耦。
package inferenceparams

// Policy controls how gateway defaults interact with client JSON bodies.
type Policy struct {
	Mode string `json:"mode"` // client_first | gateway_only
}

const (
	ModeClientFirst   = "client_first"
	ModeGatewayOnly   = "gateway_only"
	KeyLlamaCpp       = "inference_params_llamacpp"
	KeyOllama         = "inference_params_ollama"
	KeyPolicy         = "inference_params_policy" // legacy global
	KeyPolicyLlamaCpp = "inference_params_policy_llamacpp"
	KeyPolicyOllama   = "inference_params_policy_ollama"
)

// Params holds optional overrides; nil/omitted fields mean "do not inject".
type Params struct {
	MaxTokens        *int     `json:"max_tokens,omitempty"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	TopK             *int     `json:"top_k,omitempty"`
	MinP             *float64 `json:"min_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty"`
	RepeatPenalty    *float64 `json:"repeat_penalty,omitempty"`
	Seed             *int     `json:"seed,omitempty"`
	EnableThinking   *string  `json:"enable_thinking,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	KeepAlive        *string  `json:"keep_alive,omitempty"`
	MaxOutputCap     *int     `json:"max_output_cap,omitempty"`
}

// Field describes one admin form control.
type Field struct {
	Key         string            `json:"key"`
	Label       string            `json:"label"`
	Hint        string            `json:"hint"`
	Description string            `json:"description"`
	Impact      string            `json:"impact"`
	Range       string            `json:"range"`
	Recommend   string            `json:"recommend"`
	Example     string            `json:"example,omitempty"`
	Examples    map[string]string `json:"examples,omitempty"` // per backend: llamacpp | ollama
	Placeholder string            `json:"placeholder,omitempty"`
	Type        string            `json:"type"`
	Section     string            `json:"section"`
	Backends    []string          `json:"backends"`
	Select      []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"select,omitempty"`
}

func fnum(key, label, desc, impact, rng, rec, exLlama, exOllama, section string, backends []string) Field {
	ex := map[string]string{"llamacpp": exLlama, "ollama": exOllama}
	return Field{
		Key: key, Label: label, Type: "number", Section: section, Backends: backends,
		Description: desc, Impact: impact, Range: rng, Recommend: rec,
		Examples: ex, Hint: desc,
	}
}

// Schema returns admin UI field metadata.
func Schema() []Field {
	thinkingSelect := []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}{
		{Value: "", Label: "透传（不改客户端的思考字段）"},
		{Value: "true", Label: "开启"},
		{Value: "false", Label: "关闭"},
	}
	both := []string{"both"}
	llama := []string{"llamacpp"}
	ollama := []string{"ollama"}
	return []Field{
		{
			Key: "mode", Label: "参数合并策略", Type: "select", Section: "policy", Backends: both,
			Description: "决定网关保存的默认值如何与客户端请求体合并。",
			Impact:      "client_first：只填补客户端未传的字段；gateway_only：对已配置项强制覆盖客户端同名字段。",
			Range:       "client_first | gateway_only",
			Recommend:   "日常用 client_first。思考开关和采样用同一套策略：客户端写了就以客户端为准，没写才用这里的值。要强制关闭，选仅网关配置。",
			Placeholder: "client_first",
			Hint:        "合并策略",
			Select: []struct {
				Value string `json:"value"`
				Label string `json:"label"`
			}{
				{Value: ModeClientFirst, Label: "客户端优先（缺省用网关）"},
				{Value: ModeGatewayOnly, Label: "仅网关配置（忽略客户端同名字段）"},
			},
		},
		fnum("max_tokens", "max_tokens",
			"单次 completion 最多生成的 token 数上限。",
			"越大输出越长、耗时与显存占用越高；过小会截断回答。",
			"整数，通常 256～8192；具体还受模型 context 限制。",
			"对话 2048～4096；代码生成可 8192。留空表示网关不注入该字段。",
			"4096", "4096", "chat", both),
		fnum("temperature", "temperature",
			"采样温度，控制随机性。",
			"越高越发散、创意更强；越低越稳定、可复现。",
			"浮点 0～2，常用 0～1.2。",
			"稳定任务 0.2～0.5；创意写作 0.7～1.0。留空不注入。",
			"0.7", "0.8", "chat", both),
		fnum("top_p", "top_p",
			"核采样：在累计概率达到 p 的 token 集合内采样。",
			"与 temperature 配合调节多样性；过小输出单调。",
			"浮点 0～1；1 表示几乎不截断。",
			"常用 0.9～0.95。留空不注入。",
			"0.95", "0.9", "chat", both),
		fnum("top_k", "top_k",
			"每步只从概率最高的 K 个 token 中采样（llama.cpp 常见）。",
			"减小 K 输出更保守；0 通常表示不限制。",
			"整数 ≥0。",
			"llama.cpp 常用 40。留空不注入。",
			"40", "40", "chat", llama),
		fnum("min_p", "min_p",
			"相对最高 token 概率的最小阈值（llama.cpp）。",
			"过滤低概率尾巴，减少胡言；过大可能缺词。",
			"浮点 0～1。",
			"常用 0.05～0.1，或留空不传。",
			"0.05", "0.05", "chat", llama),
		fnum("frequency_penalty", "frequency_penalty",
			"OpenAI 式 frequency_penalty，惩罚已出现 token。",
			"减轻重复用词；绝对值过大可能破坏连贯。",
			"浮点，一般 -2～2，0 为关闭。",
			"默认 0；重复严重时试 0.3～0.8。",
			"0", "0", "chat", both),
		fnum("presence_penalty", "presence_penalty",
			"OpenAI 式 presence_penalty，鼓励谈论新话题。",
			"减少车轱辘话；过大可能跑题。",
			"浮点，一般 -2～2。",
			"默认 0。",
			"0", "0", "chat", both),
		fnum("repeat_penalty", "repeat_penalty",
			"重复惩罚（llama.cpp / Ollama 常见 repeat_penalty）。",
			">1 抑制重复，<1 更重复。",
			"浮点，常用 1.0～1.2。",
			"1.0～1.15；代码生成可略高。",
			"1.1", "1.1", "chat", both),
		fnum("seed", "seed",
			"随机数种子，便于在相同参数下尽量复现输出。",
			"仍受并行、量化、上游实现影响，非绝对 deterministic。",
			"整数；留空表示每次随机。",
			"调试/对比实验时填固定整数。",
			"", "", "chat", both),
		{
			Key: "enable_thinking", Label: "思考开关", Type: "select", Section: "chat", Backends: both,
			Description: "与合并策略一起决定思考。相关字段会一次算完：reasoning、reasoning_effort、thinking、think、enable_thinking、chat_template_kwargs.enable_thinking，以及 include 里的 reasoning.*。",
			Impact:      "最终关闭时这些打开思考的字段会一起去掉。最终开启而上游不支持时，由上游返回错误。",
			Range:       "关闭 | 开启 | 透传",
			Recommend:   "Codex 会自带 reasoning。客户端优先时以它为准。模型不支持思考时，把合并策略改成仅网关配置并保持关闭。",
			Example:     "false",
			Hint:        "思考开关",
			Select:      thinkingSelect,
		},
		{
			Key: "stop", Label: "stop", Type: "stop_lines", Section: "chat", Backends: both,
			Description: "遇到这些字符串时停止生成；每行一条。",
			Impact:      "可提前结束输出；过多/过短 stop 可能误触发。",
			Range:       "字符串数组，每行一条。",
			Recommend:   "按需填写，如 ###、<|end|>；留空不注入。",
			Example:     "###",
			Hint:        "停止序列",
		},
		{
			Key: "keep_alive", Label: "keep_alive", Type: "text", Section: "load", Backends: ollama,
			Description: "Ollama 加载模型后在内存中保留的时长（管理端 load 时使用）。",
			Impact:      "长 keep_alive 减少下次 TTFT，但占用内存；0 表示尽快卸载。",
			Range:       "时长字符串，如 24h、30m、0。",
			Recommend:   "服务化部署常用 24h；资源紧张用 5m 或 0。",
			Example:     "24h",
			Hint:        "Ollama keep_alive",
		},
		fnum("max_output_cap", "max_output_cap",
			"单次输出 token 上限。同时裁剪 max_tokens 和 Responses 的 max_output_tokens。",
			"客户端要得再大也会被截到这个值。出厂来自 config.yaml 的 max_output_tokens。",
			"整数 >0。",
			"给 Codex 写代码常用 8192。",
			"8192", "8192", "gateway", both),
	}
}

// SectionTitles for grouped UI.
func SectionTitles() map[string]string {
	return map[string]string{
		"policy":  "合并策略",
		"chat":    "Chat · 采样",
		"load":    "加载与保活",
		"gateway": "网关整理",
	}
}

// SectionDescriptions explains each settings group.
func SectionDescriptions() map[string]string {
	return map[string]string{
		"policy":  "控制「网关默认值」与「客户端 JSON」谁说了算。保存后立即影响经网关转发的 /v1 请求（在 sanitize 之后合并）。",
		"chat":    "OpenAI 兼容 chat/completions 常见采样字段。留空表示网关不向请求体写入该字段（完全透传客户端）。",
		"load":    "只影响 Ollama 加载模型时在内存里保留多久，不写入 /v1 请求。",
		"gateway": "网关自己的输出长度保护，会同时作用在 max_tokens 和 max_output_tokens 上。",
	}
}
