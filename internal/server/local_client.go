package server

import (
	"fmt"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
)

// localClientEntry 按后端 ID 缓存 HTTP 客户端和 BaseURL，保持连接池稳定并避免跨后端串线。
type localClientEntry struct {
	baseURL string
	client  *llama.Client
}

// localClientForBackend 返回指定本地后端的客户端，不读取当前活动 Provider 的 s.llama 指针。
func (s *Server) localClientForBackend(backend string) (*llama.Client, error) {
	if s == nil || s.cfg == nil {
		return nil, fmt.Errorf("本地 Provider 客户端尚未配置")
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	if !isLocalInferenceBackend(backend) {
		return nil, fmt.Errorf("Provider %q 不支持本地 HTTP 客户端", backend)
	}
	baseURL := strings.TrimSpace(s.baseURLForBackend(backend))
	if baseURL == "" {
		return nil, fmt.Errorf("Provider %q BaseURL 未配置", backend)
	}
	s.localClientsMu.Lock()
	defer s.localClientsMu.Unlock()
	if entry, ok := s.localClients[backend]; ok && entry.client != nil && entry.baseURL == baseURL {
		return entry.client, nil
	}
	client := s.newUpstreamClient(baseURL, backend)
	if s.localClients == nil {
		s.localClients = make(map[string]localClientEntry)
	}
	s.localClients[backend] = localClientEntry{baseURL: baseURL, client: client}
	return client, nil
}
