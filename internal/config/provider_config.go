package config

import (
	"fmt"
	"regexp"
	"strings"

	gatewayprovider "github.com/whl/llm-gateway/internal/provider"
)

// OpenAICompatibleProviderConfig 从 deploy/config.yaml 读取，API Key 应通过环境变量注入。
// 远程凭据不写入 gateway_settings，切换当前后端只保存 Provider ID 与模型 ID。
type OpenAICompatibleProviderConfig struct {
	ID                  string `yaml:"id"`
	Enabled             bool   `yaml:"enabled"`
	BaseURL             string `yaml:"base_url"`
	APIKey              string `yaml:"api_key"`
	APIKeyHeader        string `yaml:"api_key_header"`
	DefaultModel        string `yaml:"default_model"`
	AllowInsecureHTTP   bool   `yaml:"allow_insecure_http"`
	AllowPrivateNetwork bool   `yaml:"allow_private_network"`
	MaxResponseBytes    int64  `yaml:"max_response_bytes"`
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func (c *Config) validateProviderConfigs() error {
	seen := map[string]bool{"llamacpp": true, "ollama": true}
	for i := range c.Providers.OpenAICompatible {
		item := &c.Providers.OpenAICompatible[i]
		item.ID = strings.ToLower(strings.TrimSpace(item.ID))
		item.BaseURL = strings.TrimRight(strings.TrimSpace(item.BaseURL), "/")
		item.APIKey = strings.TrimSpace(item.APIKey)
		item.APIKeyHeader = strings.TrimSpace(item.APIKeyHeader)
		item.DefaultModel = strings.TrimSpace(item.DefaultModel)
		if !providerIDPattern.MatchString(item.ID) {
			return fmt.Errorf("providers.openai_compatible[%d].id is invalid", i)
		}
		if seen[item.ID] {
			return fmt.Errorf("providers.openai_compatible[%d] duplicates Provider ID %q", i, item.ID)
		}
		seen[item.ID] = true
		if !item.Enabled {
			continue
		}
		if item.DefaultModel == "" {
			return fmt.Errorf("providers.openai_compatible[%d] (%s): default_model is required when enabled", i, item.ID)
		}
		if strings.Contains(item.BaseURL, "${") || strings.Contains(item.APIKey, "${") {
			return fmt.Errorf("providers.openai_compatible[%d] (%s): base_url/API key environment variable is unresolved", i, item.ID)
		}
		_, err := gatewayprovider.NewOpenAICompatibleProvider(gatewayprovider.OpenAICompatibleConfig{
			ID: item.ID, BaseURL: item.BaseURL, APIKey: item.APIKey, APIKeyHeader: item.APIKeyHeader,
			AllowInsecureHTTP: item.AllowInsecureHTTP, AllowPrivateNetwork: item.AllowPrivateNetwork,
			MaxResponseBytes: item.MaxResponseBytes,
		})
		if err != nil {
			return fmt.Errorf("providers.openai_compatible[%d] (%s): %w", i, item.ID, err)
		}
	}
	return nil
}
