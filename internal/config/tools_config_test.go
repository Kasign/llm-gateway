package config

import "testing"

func TestToolsConfigDefaultsDisabled(t *testing.T) {
	var cfg ToolsConfig
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.Enabled || len(cfg.AllowedTools) != 0 {
		t.Fatalf("zero-value ToolsConfig should be disabled with no tools: %+v", cfg)
	}
}

func TestToolsConfigNormalizesToolAllowlist(t *testing.T) {
	cfg := ToolsConfig{
		Enabled: true,
		AllowedTools: []string{" web.search "},
		SearXNG: SearXNGConfig{BaseURL: " https://search.example.test "},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if cfg.AllowedTools[0] != "web.search" || cfg.SearXNG.BaseURL != "https://search.example.test" {
		t.Fatalf("tool config not normalized: %+v", cfg)
	}
}

func TestToolsConfigRequiresExplicitToolAllowlist(t *testing.T) {
	cases := []struct {
		name string
		cfg  ToolsConfig
	}{
		{name: "no tools", cfg: ToolsConfig{Enabled: true}},
		{name: "missing search URL", cfg: ToolsConfig{Enabled: true, AllowedTools: []string{"web.search"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Normalize(); err == nil {
				t.Fatal("invalid enabled tools config was accepted")
			}
		})
	}
}

func TestToolsConfigRejectsDuplicateAndInvalidToolEntries(t *testing.T) {
	cases := []ToolsConfig{
		{AllowedTools: []string{"web.search", "web.search"}},
		{AllowedTools: []string{"Web.Search"}},
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

func TestBenchmarkIsolatedEnvironmentDefaultsDisabled(t *testing.T) {
	var cfg Config
	if cfg.Benchmark.IsolatedEnvironment {
		t.Fatal("benchmark isolated environment must default to disabled")
	}
}
