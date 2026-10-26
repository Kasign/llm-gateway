package server

import (
	"context"
	"fmt"
	"strings"

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
	if id := strings.ToLower(strings.TrimSpace(st.ServiceProviderID)); id != "" {
		return id
	}
	if s == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(s.effectiveUpstream().Backend))
}

func (s *Server) remoteProviderConfig(id string) (config.OpenAICompatibleProviderConfig, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if s == nil || s.cfg == nil { return config.OpenAICompatibleProviderConfig{}, false }
	// 部署配置（包括禁用项）优先占有 ID，Web 项绝不覆盖。
	for _, item := range s.cfg.Providers.OpenAICompatible {
		if strings.EqualFold(item.ID, id) { return item, item.Enabled }
	}
	s.providerConfigMu.RLock()
	item, ok := s.webProviders[id]
	s.providerConfigMu.RUnlock()
	if !ok || !item.Enabled || item.SourceConflict || item.SecretUnavailable {
		return config.OpenAICompatibleProviderConfig{}, false
	}
	return config.OpenAICompatibleProviderConfig{
		ID:item.ID, Enabled:item.Enabled, BaseURL:item.BaseURL, APIKey:item.APIKey,
		APIKeyHeader:item.APIKeyHeader, DefaultModel:item.DefaultModel, ModelMappings:cloneStringMap(item.ModelMappings),
		AllowInsecureHTTP:item.AllowInsecureHTTP, AllowPrivateNetwork:item.AllowPrivateNetwork,
		MaxResponseBytes:item.MaxResponseBytes, MaxConcurrentRequests:item.MaxConcurrentRequests,
		RequestTimeoutSeconds:item.RequestTimeoutSeconds,
	}, true
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

// availableBackends 仅暴露安全配置的元数据，不返回 API Key。
func (s *Server) availableBackends() []map[string]interface{} {
	out := []map[string]interface{}{
		{"id":"llamacpp","label":"llama.cpp","kind":"local","base_url":s.baseURLForBackend("llamacpp"),"source":"deployment","editable":false},
		{"id":"ollama","label":"Ollama","kind":"local","base_url":s.baseURLForBackend("ollama"),"source":"deployment","editable":false},
	}
	if s == nil || s.cfg == nil { return out }
	for _, item := range s.cfg.Providers.OpenAICompatible {
		if !item.Enabled { continue }
		out = append(out, map[string]interface{}{"id":item.ID,"label":item.ID,"kind":"remote","base_url":item.BaseURL,"default_model":item.DefaultModel,"api_key_configured":item.APIKey!="","source":"deployment","editable":false})
	}
	s.providerConfigMu.RLock()
	rows := make([]managedProvider,0,len(s.webProviders))
	for _, row := range s.webProviders { rows = append(rows,row) }
	s.providerConfigMu.RUnlock()
	for _, row := range rows {
		if !row.Enabled || row.SourceConflict || row.SecretUnavailable { continue }
		out = append(out,map[string]interface{}{"id":row.ID,"label":row.ID,"kind":"remote","base_url":row.BaseURL,"default_model":row.DefaultModel,"api_key_configured":row.APIKeyCiphertext!=""||row.APIKey!="","source":"web","editable":true})
	}
	return out
}
