package config

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpandConfigEnvPreservesPbkdf2Hash(t *testing.T) {
	hash := "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk"
	os.Setenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH", hash)
	defer os.Unsetenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH")

	fromEnv := expandConfigEnv(`admin_password_hash: "${LLM_GATEWAY_ADMIN_PASSWORD_HASH}"`)
	if !strings.Contains(fromEnv, hash) {
		t.Fatalf("env substitution broken:\n%s", fromEnv)
	}

	literal := expandConfigEnv(`admin_password_hash: "` + hash + `"`)
	if literal != `admin_password_hash: "`+hash+`"` {
		t.Fatalf("literal hash corrupted:\n%s", literal)
	}
}

func TestExpandConfigEnvLoadsRemoteProviderKeyBeforeValidation(t *testing.T) {
	const envName = "LLM_GATEWAY_TEST_PROVIDER_API_KEY"
	t.Setenv(envName, "sk-test-remote-key")
	raw := `providers:
  openai_compatible:
    - id: deepseek
      enabled: true
      base_url: "https://api.deepseek.com/v1"
      api_key: "${LLM_GATEWAY_TEST_PROVIDER_API_KEY}"
      api_key_header: "Authorization"
      default_model: "deepseek-chat"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(expandConfigEnv(raw)), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(cfg.Providers.OpenAICompatible) != 1 {
		t.Fatalf("provider count = %d, want 1", len(cfg.Providers.OpenAICompatible))
	}
	if got := cfg.Providers.OpenAICompatible[0].APIKey; got != "sk-test-remote-key" {
		t.Fatalf("API key env expansion = %q, want configured test value", got)
	}
	if err := cfg.validateProviderConfigs(); err != nil {
		t.Fatalf("validate provider config after env expansion: %v", err)
	}
}

func TestRemoteProviderRejectsUnresolvedEnvironmentPlaceholder(t *testing.T) {
	raw := `providers:
  openai_compatible:
    - id: deepseek
      enabled: true
      base_url: "https://api.deepseek.com/v1"
      api_key: "${LLM_GATEWAY_TEST_UNSET_PROVIDER_KEY}"
      default_model: "deepseek-chat"
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(expandConfigEnv(raw)), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if err := cfg.validateProviderConfigs(); err == nil {
		t.Fatal("enabled Provider with unresolved API Key placeholder must fail configuration validation")
	}
}
