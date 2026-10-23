package config

import "testing"

func TestValidateAgentRoutesSupportsMultipleProviders(t *testing.T) {
	cfg := &Config{}
	cfg.Llama.LlamaCppBaseURL = "http://llama:8080"
	cfg.Llama.OllamaBaseURL = "http://ollama:11434"
	cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
	}}
	cfg.Agents = []AgentRouteConfig{
		{ID: "ollama", Enabled: true, PathPrefix: "/ollama", ProviderID: "ollama", DefaultModel: "qwen3:8b"},
		{ID: "llama", Enabled: true, PathPrefix: "/llama", ProviderID: "llamacpp", DefaultModel: "local-model.gguf"},
		{ID: "deepseek-agent", Enabled: true, PathPrefix: "/deepseek", ProviderID: "deepseek", DefaultModel: "deepseek-chat"},
	}
	if err := cfg.validateProviderConfigs(); err != nil {
		t.Fatalf("valid multi-Provider routes rejected: %v", err)
	}
	if cfg.Agents[2].ProviderID != "deepseek" || cfg.Agents[0].PathPrefix != "/ollama" {
		t.Fatalf("route fields not normalized: %#v", cfg.Agents)
	}
}

func TestValidateAgentRoutesRejectsInvalidRoutes(t *testing.T) {
	base := func(route AgentRouteConfig) *Config {
		cfg := &Config{}
		cfg.Llama.LlamaCppBaseURL = "http://llama:8080"
		cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{
			ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
		}}
		cfg.Agents = []AgentRouteConfig{route}
		return cfg
	}
	cases := []struct {
		name string
		route AgentRouteConfig
	}{
		{name: "reserved prefix", route: AgentRouteConfig{ID: "bad", Enabled: true, PathPrefix: "/api", ProviderID: "deepseek", DefaultModel: "deepseek-chat"}},
		{name: "nested prefix", route: AgentRouteConfig{ID: "bad", Enabled: true, PathPrefix: "/agent/deepseek", ProviderID: "deepseek", DefaultModel: "deepseek-chat"}},
		{name: "unknown provider", route: AgentRouteConfig{ID: "bad", Enabled: true, PathPrefix: "/bad", ProviderID: "unknown", DefaultModel: "model"}},
		{name: "missing model", route: AgentRouteConfig{ID: "bad", Enabled: true, PathPrefix: "/bad", ProviderID: "deepseek"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := base(tc.route).validateProviderConfigs(); err == nil {
				t.Fatal("invalid Agent route was accepted")
			}
		})
	}
}

func TestValidateAgentRoutesRejectsDuplicatePrefix(t *testing.T) {
	cfg := &Config{}
	cfg.Llama.LlamaCppBaseURL = "http://llama:8080"
	cfg.Agents = []AgentRouteConfig{
		{ID: "local-a", Enabled: true, PathPrefix: "/local", ProviderID: "llamacpp", DefaultModel: "a.gguf"},
		{ID: "local-b", Enabled: true, PathPrefix: "/local", ProviderID: "llamacpp", DefaultModel: "b.gguf"},
	}
	if err := cfg.validateAgentRouteConfigs(); err == nil {
		t.Fatal("duplicate path prefix was accepted")
	}
}
