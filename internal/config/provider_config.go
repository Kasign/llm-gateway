package config

import (
	"fmt"
	"regexp"
	"strings"
)

const MaxProviderRequestsPerMinute = 100000

// MaxProviderTokenBudgetPerDay 是单个远程 Provider 的 UTC 每日 Token 预算上限；0 表示关闭。
const MaxProviderTokenBudgetPerDay int64 = 1000000000000

// OpenAICompatibleProviderConfig 是 Web 管理 Provider 的运行时快照。
// 权威配置保存在 gateway_settings，API Key 在写入数据库前加密。
type OpenAICompatibleProviderConfig struct {
	ID                  string `yaml:"id"`
	Enabled             bool   `yaml:"enabled"`
	BaseURL             string `yaml:"base_url"`
	APIKey              string `yaml:"api_key"`
	APIKeyHeader        string `yaml:"api_key_header"`
	DefaultModel        string            `yaml:"default_model"`
	ModelMappings       map[string]string `yaml:"model_mappings"`
	AllowInsecureHTTP   bool   `yaml:"allow_insecure_http"`
	AllowPrivateNetwork bool   `yaml:"allow_private_network"`
	MaxResponseBytes        int64  `yaml:"max_response_bytes"`
	MaxConcurrentRequests   int    `yaml:"max_concurrent_requests"`
	MaxQueueSize            int    `yaml:"max_queue_size"`
	QueueWaitTimeoutSeconds int    `yaml:"queue_wait_timeout_seconds"`
	RequestsPerMinute       int    `yaml:"requests_per_minute"`
	TokenBudgetPerDay      int64 `yaml:"token_budget_per_day"`
	RequestTimeoutSeconds  int   `yaml:"request_timeout_seconds"`
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

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
		if route.ProviderID == "llamacpp" || route.ProviderID == "ollama" {
			if c.BaseURLForBackend(route.ProviderID) == "" {
				return fmt.Errorf("agents[%d] (%s): Provider %q has no configured base URL", i, route.ID, route.ProviderID)
			}
		}
		// 远程 Provider 统一由 Web 管理台保存；配置加载阶段尚未读取数据库，
		// Provider 是否存在/启用由请求路由时核验。
	}
	return nil
}
