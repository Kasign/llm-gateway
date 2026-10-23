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
	MaxConcurrentRequests int  `yaml:"max_concurrent_requests"`
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
		if item.MaxConcurrentRequests < 0 || item.MaxConcurrentRequests > gatewayprovider.MaxOpenAICompatibleConcurrentRequests {
			return fmt.Errorf("providers.openai_compatible[%d] (%s): max_concurrent_requests must be between 0 and %d", i, item.ID, gatewayprovider.MaxOpenAICompatibleConcurrentRequests)
		}
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
			MaxConcurrentRequests: item.MaxConcurrentRequests,
		})
		if err != nil {
			return fmt.Errorf("providers.openai_compatible[%d] (%s): %w", i, item.ID, err)
		}
	}
	return c.validateAgentRouteConfigs()
}


// AgentRouteConfig 定义一个独立的 OpenAI-compatible 入口。
// 多个 Agent 可同时固定到不同 Provider，不依赖管理台的全局活动上游。
type AgentRouteConfig struct {
	ID           string `yaml:"id"`
	Enabled      bool   `yaml:"enabled"`
	PathPrefix   string `yaml:"path_prefix"`
	ProviderID   string `yaml:"provider_id"`
	DefaultModel string `yaml:"default_model"`
}

var agentPathPrefixPattern = regexp.MustCompile(`^/[a-z0-9][a-z0-9._-]{0,63}$`)

// validateAgentRouteConfigs 在启动时检查 Agent ID、路径和 Provider 引用。
// 禁止路由覆盖网关保留路径，防止同一 URL 在管理 API 与推理路由间发生歧义。
func (c *Config) validateAgentRouteConfigs() error {
	seenIDs := make(map[string]bool)
	seenPrefixes := make(map[string]bool)
	for i := range c.Agents {
		route := &c.Agents[i]
		route.ID = strings.ToLower(strings.TrimSpace(route.ID))
		route.PathPrefix = strings.TrimSpace(route.PathPrefix)
		route.ProviderID = strings.ToLower(strings.TrimSpace(route.ProviderID))
		route.DefaultModel = strings.TrimSpace(route.DefaultModel)

		if !providerIDPattern.MatchString(route.ID) {
			return fmt.Errorf("agents[%d].id is invalid", i)
		}
		if !agentPathPrefixPattern.MatchString(route.PathPrefix) {
			return fmt.Errorf("agents[%d].path_prefix must be a single lowercase path segment such as /ollama", i)
		}
		switch route.PathPrefix {
		case "/api", "/v1", "/static", "/health":
			return fmt.Errorf("agents[%d].path_prefix conflicts with a reserved gateway path", i)
		}
		if !providerIDPattern.MatchString(route.ProviderID) {
			return fmt.Errorf("agents[%d].provider_id is invalid", i)
		}
		if seenIDs[route.ID] {
			return fmt.Errorf("agents[%d] duplicates Agent ID %q", i, route.ID)
		}
		if seenPrefixes[route.PathPrefix] {
			return fmt.Errorf("agents[%d] duplicates path_prefix %q", i, route.PathPrefix)
		}
		seenIDs[route.ID] = true
		seenPrefixes[route.PathPrefix] = true
		if !route.Enabled {
			continue
		}
		if route.DefaultModel == "" || strings.Contains(route.DefaultModel, "$"+"{") || strings.ContainsAny(route.DefaultModel, "\r\n") {
			return fmt.Errorf("agents[%d] (%s): default_model is required and must be a resolved single line", i, route.ID)
		}
		switch route.ProviderID {
		case "llamacpp", "ollama":
			if c.BaseURLForBackend(route.ProviderID) == "" {
				return fmt.Errorf("agents[%d] (%s): Provider %q has no configured base URL", i, route.ID, route.ProviderID)
			}
		default:
			found := false
			for _, configured := range c.Providers.OpenAICompatible {
				if configured.Enabled && configured.ID == route.ProviderID {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("agents[%d] (%s): Provider %q is not configured and enabled", i, route.ID, route.ProviderID)
			}
		}
	}
	return nil
}
