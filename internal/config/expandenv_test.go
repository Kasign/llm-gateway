package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpandConfigEnvPreservesPbkdf2Hash(t *testing.T) {
	hash := "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk"
	t.Setenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH", hash)

	fromEnv := expandConfigEnv(`admin_password_hash: "${LLM_GATEWAY_ADMIN_PASSWORD_HASH}"`)
	var decoded struct {
		AdminPasswordHash string `yaml:"admin_password_hash"`
	}
	if err := yaml.Unmarshal([]byte(fromEnv), &decoded); err != nil {
		t.Fatalf("expanded YAML should remain valid: %v", err)
	}
	if decoded.AdminPasswordHash != hash {
		t.Fatalf("env substitution = %q, want exact PBKDF2 hash", decoded.AdminPasswordHash)
	}

	literal := expandConfigEnv(`admin_password_hash: "` + hash + `"`)
	if err := yaml.Unmarshal([]byte(literal), &decoded); err != nil {
		t.Fatalf("literal YAML should remain valid: %v", err)
	}
	if decoded.AdminPasswordHash != hash {
		t.Fatalf("literal PBKDF2 hash changed: got %q", decoded.AdminPasswordHash)
	}
}

func TestExpandConfigEnvEscapesYAMLScalarValues(t *testing.T) {
	const envName = "LLM_GATEWAY_TEST_CONFIG_SCALAR"
	value := "quoted: \"value\" # not a comment\n  injected_key: not-a-new-field \\ literal"
	t.Setenv(envName, value)
	raw := `auth:
  admin_password: "${LLM_GATEWAY_TEST_CONFIG_SCALAR}"
  admin_api_key: "original-key"
  "${LLM_GATEWAY_TEST_CONFIG_SCALAR}": "fixed-config-key"
`
	expanded := expandConfigEnv(raw)
	var decoded struct {
		Auth map[string]string `yaml:"auth"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &decoded); err != nil {
		t.Fatalf("expanded value must not corrupt YAML syntax: %v\n%s", err, expanded)
	}
	if decoded.Auth["admin_password"] != value {
		t.Fatalf("expanded scalar changed: got %q want %q", decoded.Auth["admin_password"], value)
	}
	if decoded.Auth["admin_api_key"] != "original-key" {
		t.Fatalf("environment scalar injected another YAML field: admin_api_key=%q", decoded.Auth["admin_api_key"])
	}
	if decoded.Auth["${LLM_GATEWAY_TEST_CONFIG_SCALAR}"] != "fixed-config-key" {
		t.Fatalf("environment substitution unexpectedly changed a mapping key: %#v", decoded.Auth)
	}
	if _, exists := decoded.Auth["quoted: \"value\" # not a comment\n  injected_key: not-a-new-field \\ literal"]; exists {
		t.Fatal("environment value was incorrectly interpreted as a mapping key")
	}
}

func TestConfigLoadRejectsUnresolvedRequiredSecrets(t *testing.T) {
	cases := []struct {
		name     string
		adminKey string
		dbPass   string
		wantPath string
	}{
		{
			name: "admin API key", adminKey: "${LLM_GATEWAY_MISSING_ADMIN_KEY}",
			dbPass: `"db-secret"`, wantPath: "auth.admin_api_key",
		},
		{
			name: "database password", adminKey: "real-admin-key",
			dbPass: `"${LLM_GATEWAY_MISSING_DB_PASSWORD}"`, wantPath: "database.password",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configText := fmt.Sprintf(`server:
  host: "127.0.0.1"
auth:
  admin_api_key: %q
  admin_username: "admin"
  admin_password: "strong-test-password"
database:
  host: "localhost"
  port: 3306
  name: "llm_gateway"
  user: "llm_gateway"
  password: %s
  auto_init: false
redis:
  addr: "127.0.0.1:6379"
  password: ""
llama:
  backend: "llamacpp"
  llamacpp_base_url: "http://127.0.0.1:8080"
  ollama_base_url: "${LLM_GATEWAY_MISSING_OPTIONAL_OLLAMA_URL}"
storage:
  models_dir: "/tmp/llm-gateway-models"
providers:
  openai_compatible: []
`, tc.adminKey, tc.dbPass)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load accepted a required secret placeholder that was not expanded")
			}
			if !strings.Contains(err.Error(), tc.wantPath) {
				t.Fatalf("error %q should identify field %q without revealing its value", err, tc.wantPath)
			}
		})
	}
}

func TestConfigLoadValidatesLocalInferenceBaseURLs(t *testing.T) {
	cases := []struct {
		name           string
		llamaURL       string
		ollamaURL      string
		wantField      string
		wantSecret     string
		wantSuccessful bool
	}{
		{
			name: "llama userinfo", llamaURL: "http://user:top-secret@127.0.0.1:8080",
			ollamaURL: "", wantField: "llama.llamacpp_base_url", wantSecret: "top-secret",
		},
		{
			name: "llama query", llamaURL: "http://127.0.0.1:8080/v1?token=private-token",
			ollamaURL: "", wantField: "llama.llamacpp_base_url", wantSecret: "private-token",
		},
		{
			name: "llama fragment", llamaURL: "http://127.0.0.1:8080/v1#internal",
			ollamaURL: "", wantField: "llama.llamacpp_base_url", wantSecret: "internal",
		},
		{
			name: "unsupported scheme", llamaURL: "file:///tmp/llama",
			ollamaURL: "", wantField: "llama.llamacpp_base_url",
		},
		{
			name: "inactive ollama with userinfo", llamaURL: "http://llama-cpp:8080/v1",
			ollamaURL: "http://user:ollama-secret@ollama:11434",
			wantField: "llama.ollama_base_url", wantSecret: "ollama-secret",
		},
		{
			name: "valid path prefix and empty inactive backend", llamaURL: "http://llama-cpp:8080/openai/v1",
			ollamaURL: "", wantSuccessful: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configText := fmt.Sprintf(`server:
  host: "127.0.0.1"
auth:
  admin_api_key: "admin-key-for-config-test"
  admin_username: "admin"
  admin_password: "strong-test-password"
database:
  host: "localhost"
  port: 3306
  name: "llm_gateway"
  user: "llm_gateway"
  password: "db-secret"
  auto_init: false
redis:
  addr: "127.0.0.1:6379"
  password: ""
llama:
  backend: "llamacpp"
  llamacpp_base_url: %q
  ollama_base_url: %q
storage:
  models_dir: "/tmp/llm-gateway-models"
providers:
  openai_compatible: []
`, tc.llamaURL, tc.ollamaURL)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(configText), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if tc.wantSuccessful {
				if err != nil {
					t.Fatalf("Load rejected valid URL configuration: %v", err)
				}
				if loaded.BaseURLForBackend("llamacpp") != "http://llama-cpp:8080/openai/v1" {
					t.Fatalf("BaseURLForBackend() = %q", loaded.BaseURLForBackend("llamacpp"))
				}
				return
			}
			if err == nil {
				t.Fatal("Load accepted a local BaseURL with unsafe URL components")
			}
			if !strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("error %q should identify field %q", err, tc.wantField)
			}
			if tc.wantSecret != "" && strings.Contains(err.Error(), tc.wantSecret) {
				t.Fatalf("configuration error leaked URL credential/query detail %q: %v", tc.wantSecret, err)
			}
		})
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
