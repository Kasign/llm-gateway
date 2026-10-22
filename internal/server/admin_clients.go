// server（admin_clients）：按后端类型构造管理用的 llama.Client，并同步更新「当前代理模型」设置。
package server

import (
	"context"
	"errors"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
)

func (s *Server) llamacppAdminClient() (*llama.Client, string, error) {
	base := strings.TrimSpace(s.baseURLForBackend("llamacpp"))
	if base == "" {
		return nil, "", errors.New("未配置 LLAMA_CPP_BASE_URL")
	}
	return s.newUpstreamClient(base, "llamacpp"), base, nil
}

func (s *Server) adminClientForBackend(backend string) (*llama.Client, string, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "ollama":
		base := strings.TrimSpace(s.baseURLForBackend("ollama"))
		if base == "" {
			return nil, "", errors.New("未配置 OLLAMA_BASE_URL")
		}
		return s.newUpstreamClient(base, "ollama"), base, nil
	case "llamacpp", "":
		return s.llamacppAdminClient()
	default:
		return nil, "", errors.New("backend must be llamacpp or ollama")
	}
}

func (s *Server) maybeUpdateActiveUpstreamModel(ctx context.Context, backend, modelName string) {
	modelName = strings.TrimSpace(modelName)
	backend = strings.ToLower(strings.TrimSpace(backend))
	if modelName == "" || backend == "" {
		return
	}
	if s.effectiveUpstream().Backend != backend {
		return
	}
	u := s.effectiveUpstream()
	u.ModelName = modelName
	if u.ModelsByBackend == nil {
		u.ModelsByBackend = map[string]string{}
	}
	u.ModelsByBackend[backend] = modelName
	if s.settings != nil {
		_ = s.settings.SaveUpstream(ctx, u)
	}
	s.applyUpstream(u, backend == "llamacpp")
}
