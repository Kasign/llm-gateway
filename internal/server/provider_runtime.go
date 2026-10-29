package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
)

// isLocalInferenceBackend 表示需要本地模型加载/卸载控制 API 的后端。
func isLocalInferenceBackend(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "llamacpp", "ollama":
		return true
	default:
		return false
	}
}

// providerIDForState 优先使用 Resolve 阶段固定的 Provider；旧内部调用未提供时回退到活动后端。
func (s *Server) providerIDForState(st core.State) string {
	if id := strings.ToLower(strings.TrimSpace(st.Model.ProviderID)); id != "" {
		return id
	}
	// 内部调用带 ServiceModel 时，Resolver 优先使用 ServiceProviderID；
	// 若未显式指定，就回退全局活动 Provider，而不是取 Agent 前缀。
	if strings.TrimSpace(st.ServiceModel) != "" {
		if id := strings.ToLower(strings.TrimSpace(st.ServiceProviderID)); id != "" {
			return id
		}
		if s == nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(s.effectiveUpstream().Backend))
	}
	if id := strings.ToLower(strings.TrimSpace(st.RouteProviderID)); id != "" {
		return id
	}
	if id := strings.ToLower(strings.TrimSpace(st.ServiceProviderID)); id != "" {
		return id
	}
	if s == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s.effectiveUpstream().Backend))
}

func managedProviderConfig(item managedProvider) config.OpenAICompatibleProviderConfig {
	return config.OpenAICompatibleProviderConfig{
		ID: item.ID, Enabled: item.Enabled, BaseURL: item.BaseURL, APIKey: item.APIKey,
		APIKeyHeader: item.APIKeyHeader, DefaultModel: item.DefaultModel,
		ModelMappings: cloneStringMap(item.ModelMappings),
		AllowInsecureHTTP: item.AllowInsecureHTTP, AllowPrivateNetwork: item.AllowPrivateNetwork,
		MaxResponseBytes: item.MaxResponseBytes, MaxConcurrentRequests: item.MaxConcurrentRequests,
		MaxQueueSize: item.MaxQueueSize, QueueWaitTimeoutSeconds: item.QueueWaitTimeoutSeconds,
		RequestTimeoutSeconds: item.RequestTimeoutSeconds,
		RequestsPerMinute: item.RequestsPerMinute, TokenBudgetPerDay: item.TokenBudgetPerDay,
	}
}

func (s *Server) remoteProviderConfig(id string) (config.OpenAICompatibleProviderConfig, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if s == nil || id == "" {
		return config.OpenAICompatibleProviderConfig{}, false
	}
	s.providerConfigMu.RLock()
	row, ok := s.webProviders[id]
	s.providerConfigMu.RUnlock()
	if !ok || !row.Enabled || row.SecretUnavailable {
		return config.OpenAICompatibleProviderConfig{}, false
	}
	return managedProviderConfig(row), true
}
func (s *Server) isConfiguredProvider(id string) bool {
	if isLocalInferenceBackend(id) {
		return true
	}
	_, ok := s.remoteProviderConfig(id)
	return ok
}

// activeProviderStatus 按当前生效后端判断依赖状态，不把保留的本地管理客户端当成活动推理端。
func (s *Server) activeProviderStatus(ctx context.Context) (backend, status string, err error) {
	if s == nil {
		return "", "unavailable", fmt.Errorf("server is nil")
	}

	// 一次读取上游和本地客户端，确保切换后端时检查的是同一份运行时快照。
	s.upstreamMu.RLock()
	upstream := s.upstream
	localClient := s.llama
	s.upstreamMu.RUnlock()

	backend = strings.ToLower(strings.TrimSpace(upstream.Backend))
	if backend == "" {
		return backend, "unconfigured", fmt.Errorf("active Provider is not configured")
	}
	if isLocalInferenceBackend(backend) {
		if localClient == nil {
			return backend, "unavailable", fmt.Errorf("local Provider %q client is not configured", backend)
		}
		if err := localClient.Health(ctx); err != nil {
			return backend, err.Error(), fmt.Errorf("local Provider %q health check failed: %w", backend, err)
		}
		return backend, "ok", nil
	}

	if _, ok := s.remoteProviderConfig(backend); !ok {
		return backend, "unconfigured", fmt.Errorf("active Provider %q is not configured", backend)
	}
	if s.providers == nil {
		return backend, "unavailable", fmt.Errorf("active Provider Registry is not initialized")
	}
	if _, err := s.providers.Resolve(backend); err != nil {
		return backend, "unavailable", fmt.Errorf("active Provider %q is unavailable: %w", backend, err)
	}
	return backend, "configured; remote connectivity is checked on inference", nil
}

// availableBackends 仅返回固定本地后端和 Web 管理台已保存的远程 Provider 元数据。
func (s *Server) availableBackends() []map[string]interface{} {
	out := []map[string]interface{}{
		{"id": "llamacpp", "label": "llama.cpp", "kind": "local", "base_url": ""},
		{"id": "ollama", "label": "Ollama", "kind": "local", "base_url": ""},
	}
	if s == nil {
		return out
	}
	out[0]["base_url"] = s.baseURLForBackend("llamacpp")
	out[1]["base_url"] = s.baseURLForBackend("ollama")
	s.providerConfigMu.RLock()
	rows := make([]managedProvider, 0, len(s.webProviders))
	for _, row := range s.webProviders {
		rows = append(rows, row)
	}
	s.providerConfigMu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		if !row.Enabled || row.SecretUnavailable {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": row.ID, "label": row.ID, "kind": "remote", "base_url": row.BaseURL,
			"default_model": row.DefaultModel, "requests_per_minute": row.RequestsPerMinute,
			"token_budget_per_day": row.TokenBudgetPerDay,
			"api_key_configured": row.APIKey != "" || row.APIKeyCiphertext != "",
		})
	}
	return out
}

// providerRequestsPerMinute 返回 Web 管理台配置的 Provider RPM。0 表示不额外限制。
func (s *Server) providerRequestsPerMinute(id string) int {
	id = strings.ToLower(strings.TrimSpace(id))
	if s == nil || id == "" || isLocalInferenceBackend(id) {
		return 0
	}
	s.providerConfigMu.RLock()
	row, ok := s.webProviders[id]
	s.providerConfigMu.RUnlock()
	if !ok || !row.Enabled || row.SecretUnavailable {
		return 0
	}
	return row.RequestsPerMinute
}

// providerRateLimitGuard 在请求级 ACL 之后、保活与上游调用之前执行。
// 同一 Provider 的全局入口和多个 Agent 共用 Redis 预算；不限制本地后端，也不影响已开始的生成。
func (s *Server) providerRateLimitGuard() core.Guard {
	return func(ctx context.Context, st core.State) error {
		providerID := s.providerIDForState(st)
		rpm := s.providerRequestsPerMinute(providerID)
		if rpm <= 0 {
			return nil
		}
		if s.limiter == nil {
			return &core.Error{Status: 503, Code: "provider_rate_limit_unavailable", Message: "Provider rate limit is temporarily unavailable"}
		}
		allowed, count, err := s.limiter.AllowProvider(ctx, providerID, rpm)
		if err != nil {
			applog.Printf("[%s] Provider rate limiter unavailable: provider=%s error=%v", st.ID, providerID, err)
			return &core.Error{Status: 503, Code: "provider_rate_limit_unavailable", Message: "Provider rate limit is temporarily unavailable"}
		}
		if !allowed {
			applog.Printf("[%s] Provider rate limit exceeded: provider=%s count=%d limit=%d", st.ID, providerID, count, rpm)
			return &core.Error{Status: 429, Code: "provider_rate_limit_exceeded", Message: "Provider request rate limit exceeded"}
		}
		return nil
	}
}
