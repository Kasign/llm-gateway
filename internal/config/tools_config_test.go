package config

import "testing"

func TestToolsConfigDefaultsDisabled(t *testing.T) {
	var cfg ToolsConfig
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Enabled || len(cfg.AllowedModels) != 0 || len(cfg.AllowedTools) != 0 {
		t.Fatalf("zero-value ToolsConfig should be disabled and empty: %+v", cfg)
	}
}

func TestToolsConfigNormalizesStableModelAllowlist(t *testing.T) {
	cfg := ToolsConfig{
		Enabled: true,
		AllowedModels: []ToolModelAllowlist{
			{ProviderID: "  LlamaCPP ", ModelKey: " qwen3.5-4b "},
		},
		AllowedTools: []string{" web.search "},
		SearXNG: SearXNGConfig{
			BaseURL: " https://search.example.test ",
		},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.AllowedModels[0].ProviderID != "llamacpp" || cfg.AllowedModels[0].ModelKey != "qwen3.5-4b" {
		t.Fatalf("model identity not normalized: %+v", cfg.AllowedModels[0])
	}
	if cfg.AllowedTools[0] != "web.search" || cfg.SearXNG.BaseURL != "https://search.example.test" {
		t.Fatalf("tool config not normalized: %+v", cfg)
	}
}

func TestToolsConfigRequiresExplicitModelAndToolAllowlist(t *testing.T) {
	cases := []struct {
		name string
		cfg  ToolsConfig
	}{
		{name: "no models", cfg: ToolsConfig{Enabled: true, AllowedTools: []string{"web.search"}, SearXNG: SearXNGConfig{BaseURL: "https://search.example.test"}}},
		{name: "no tools", cfg: ToolsConfig{Enabled: true, AllowedModels: []ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "qwen"}}}},
		{name: "missing search URL", cfg: ToolsConfig{Enabled: true, AllowedModels: []ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "qwen"}}, AllowedTools: []string{"web.search"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Normalize(); err == nil {
				t.Fatal("invalid enabled tools config was accepted")
			}
		})
	}
}

func TestToolsConfigRejectsDuplicateAndInvalidAllowlistEntries(t *testing.T) {
	cases := []ToolsConfig{
		{
			AllowedModels: []ToolModelAllowlist{
				{ProviderID: "llamacpp", ModelKey: "qwen"},
				{ProviderID: "LLAMACPP", ModelKey: "qwen"},
			},
		},
		{AllowedTools: []string{"web.search", "web.search"}},
		{AllowedTools: []string{"Web.Search"}},
		{AllowedModels: []ToolModelAllowlist{{ProviderID: "bad/provider", ModelKey: "qwen"}}},
		{AllowedModels: []ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: " "}}},
	}
	for i, cfg := range cases {
		if err := cfg.Normalize(); err == nil {
			t.Fatalf("case %d: invalid config was accepted", i)
		}
	}
}

func TestToolsConfigRequiresExplicitInsecureHTTP(t *testing.T) {
	cfg := ToolsConfig{
		Enabled: true,
		AllowedModels: []ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "qwen"}},
		AllowedTools: []string{"web.search"},
		SearXNG: SearXNGConfig{BaseURL: "http://search.internal:8080"},
	}
	if err := cfg.Normalize(); err == nil {
		t.Fatal("plain HTTP without explicit opt-in was accepted")
	}
	cfg.SearXNG.AllowInsecureHTTP = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("explicit plain HTTP opt-in rejected: %v", err)
	}
}
