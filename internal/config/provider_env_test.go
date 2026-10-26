package config

import (
	"strings"
	"testing"
)

func TestApplyProviderEnvironmentLoadsMultipleProvidersAndMappings(t *testing.T) {
	keys := []string{"LLM_GATEWAY_PROVIDER_ALPHA_ENABLED", "LLM_GATEWAY_PROVIDER_ALPHA_BASE_URL", "LLM_GATEWAY_PROVIDER_ALPHA_API_KEY", "LLM_GATEWAY_PROVIDER_ALPHA_DEFAULT_MODEL", "LLM_GATEWAY_PROVIDER_ALPHA_MODEL_MAPPINGS_JSON", "LLM_GATEWAY_PROVIDER_ALPHA_REQUEST_TIMEOUT_SECONDS", "LLM_GATEWAY_PROVIDER_ALPHA_REQUESTS_PER_MINUTE", "LLM_GATEWAY_PROVIDER_BETA_BASE_URL", "LLM_GATEWAY_PROVIDER_BETA_DEFAULT_MODEL"}
	for _, key := range keys { t.Setenv(key, "") }
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_ENABLED", "true")
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_BASE_URL", "https://api.alpha.example/v1")
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_API_KEY", "secret-alpha")
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_DEFAULT_MODEL", "public-chat")
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_MODEL_MAPPINGS_JSON", `{"public-chat":"alpha-real-model"}`)
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_REQUEST_TIMEOUT_SECONDS", "900")
	t.Setenv("LLM_GATEWAY_PROVIDER_ALPHA_REQUESTS_PER_MINUTE", "35")
	t.Setenv("LLM_GATEWAY_PROVIDER_BETA_BASE_URL", "https://api.beta.example/v1")
	t.Setenv("LLM_GATEWAY_PROVIDER_BETA_DEFAULT_MODEL", "beta-chat")
	var cfg Config
	if err := applyProviderEnvironment(&cfg); err != nil { t.Fatal(err) }
	if len(cfg.Providers.OpenAICompatible) != 2 { t.Fatalf("Provider count=%d", len(cfg.Providers.OpenAICompatible)) }
	if cfg.Providers.OpenAICompatible[0].ID != "alpha" || cfg.Providers.OpenAICompatible[0].ModelMappings["public-chat"] != "alpha-real-model" { t.Fatalf("alpha config not loaded: %+v", cfg.Providers.OpenAICompatible[0]) }
	if cfg.Providers.OpenAICompatible[0].RequestTimeoutSeconds != 900 { t.Fatalf("alpha timeout=%d, want 900", cfg.Providers.OpenAICompatible[0].RequestTimeoutSeconds) }
	if cfg.Providers.OpenAICompatible[0].RequestsPerMinute != 35 { t.Fatalf("alpha requests_per_minute=%d, want 35", cfg.Providers.OpenAICompatible[0].RequestsPerMinute) }
	if cfg.Providers.OpenAICompatible[1].ID != "beta" { t.Fatalf("beta Provider ID=%q", cfg.Providers.OpenAICompatible[1].ID) }
}

func TestApplyProviderEnvironmentRejectsInvalidMappingJSONWithoutLeakingSecrets(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROVIDER_BAD_BASE_URL", "https://example.com/v1")
	t.Setenv("LLM_GATEWAY_PROVIDER_BAD_DEFAULT_MODEL", "model")
	t.Setenv("LLM_GATEWAY_PROVIDER_BAD_API_KEY", "secret-must-not-leak")
	t.Setenv("LLM_GATEWAY_PROVIDER_BAD_MODEL_MAPPINGS_JSON", "not-json")
	var cfg Config
	err := applyProviderEnvironment(&cfg)
	if err == nil || strings.Contains(err.Error(), "secret-must-not-leak") { t.Fatalf("expected safe validation error, got %v", err) }
}
