package inferenceparams

import "github.com/whl/llm-gateway/internal/config"

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }
func sp(v string) *string   { return &v }

// DefaultPolicy is the factory merge mode for a backend.
func DefaultPolicy() Policy {
	return Policy{Mode: ModeClientFirst}
}

func applyConfigGatewayFields(c *config.Config, p *Params) {
	if c == nil || p == nil {
		return
	}
	cap := c.Llama.MaxOutputTokens
	p.MaxOutputCap = &cap
}

// DefaultsLlamaCpp returns factory defaults for llama.cpp 路径（还原默认 / UI 对照）。
func DefaultsLlamaCpp(c *config.Config) Params {
	var p Params
	applyConfigGatewayFields(c, &p)
	p.EnableThinking = sp("false")
	// llama.cpp server 常见采样默认（与 Ollama 数值 intentionally 区分）
	p.Temperature = fp(0.7)
	p.TopP = fp(0.95)
	p.TopK = ip(40)
	p.RepeatPenalty = fp(1.1)
	return p
}

// DefaultsOllama returns factory defaults for Ollama 路径。
func DefaultsOllama(c *config.Config) Params {
	var p Params
	applyConfigGatewayFields(c, &p)
	p.EnableThinking = sp("false")
	p.KeepAlive = sp("24h")
	// Ollama generate API 文档常见默认
	p.Temperature = fp(0.8)
	p.TopP = fp(0.9)
	p.TopK = ip(40)
	p.RepeatPenalty = fp(1.1)
	return p
}

// DefaultsFromConfig builds both backend factory snapshots (admin API).
func DefaultsFromConfig(c *config.Config) (llama Params, ollama Params, policy Policy) {
	return DefaultsLlamaCpp(c), DefaultsOllama(c), DefaultPolicy()
}
