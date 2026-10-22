package server

import (
	"strings"

	"github.com/whl/llm-gateway/internal/config"
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

func (s *Server) remoteProviderConfig(id string) (config.OpenAICompatibleProviderConfig, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if s == nil || s.cfg == nil {
		return config.OpenAICompatibleProviderConfig{}, false
	}
	for _, item := range s.cfg.Providers.OpenAICompatible {
		if item.Enabled && strings.EqualFold(item.ID, id) {
			return item, true
		}
	}
	return config.OpenAICompatibleProviderConfig{}, false
}

func (s *Server) isConfiguredProvider(id string) bool {
	if isLocalInferenceBackend(id) {
		return true
	}
	_, ok := s.remoteProviderConfig(id)
	return ok
}

// availableBackends 仅暴露安全配置的元数据，不返回 API Key。
func (s *Server) availableBackends() []map[string]interface{} {
	out := []map[string]interface{}{
		{"id": "llamacpp", "label": "llama.cpp", "kind": "local", "base_url": s.baseURLForBackend("llamacpp")},
		{"id": "ollama", "label": "Ollama", "kind": "local", "base_url": s.baseURLForBackend("ollama")},
	}
	if s != nil && s.cfg != nil {
		for _, item := range s.cfg.Providers.OpenAICompatible {
			if !item.Enabled {
				continue
			}
			out = append(out, map[string]interface{}{
				"id": item.ID, "label": item.ID, "kind": "remote", "base_url": item.BaseURL,
				"default_model": item.DefaultModel, "api_key_configured": item.APIKey != "",
			})
		}
	}
	return out
}
