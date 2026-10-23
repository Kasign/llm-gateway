// server（admin_clients）：按后端类型构造管理用的 llama.Client，并同步更新「当前代理模型」设置。
package server

import (
	"context"
	"errors"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
	"gfunc (s *Server) llamacppAdminClient() (*llama.Client, string, error) {
	base := strings.TrimSpace(s.baseURLForBackend("llamacpp"))
	if base == "" {
		return nil, "", errors.New("未配置 LLAMA_CPP_BASE_URL")
	}
	client, err := s.localClientForBackend("llamacpp")
	if err != nil {
		return nil, "", err
	}
	return client, base, nil
}

func (s *Server) adminClientForBackend(backend string) (*llama.Client, string, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	switch backend {
	case "ollama", "llamacpp":
		base := strings.TrimSpace(s.baseURLForBackend(backend))
		if base == "" {
			if backend == "ollama" {
				return nil, "", errors.New("未配置 OLLAMA_BASE_URL")
			}
			return nil, "", errors.New("未配置 LLAMA_CPP_BASE_URL")
		}
		client, err := s.localClientForBackend(backend)
		if err != nil {
			return nil, "", err
		}
		return client, base, nil
	default:
		return nil, "", errors.New("backend must be llamacpp or ollama")
	}
}

// maybeUpdateActiveUpstreamModel 保留旧签名，供 InferenceLoad 等现有内部调用使用。
func (s *Server) maybeUpdateActiveUpstreamModel(ctx context.Context, backend, modelName string) {
	s.maybeUpdateActiveUpstreamModelForSnapshot(ctx, s.effectiveUpstream(), backend, modelName)
}

// maybeUpdateActiveUpstreamModelForSnapshot 仅在解析时的上游仍有效时采纳模型，避免迟到请求覆盖管理员切换。
func (s *Server) maybeUpdateActiveUpstreamModelForSnapshot(ctx context.Context, expected settings.Upstream, backend, modelName string) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	modelName = strings.TrimSpace(modelName)
	if backend == "" || modelName == "" || !strings.EqualFold(strings.TrimSpace(expected.Backend), backend) {
		return
	}
	s.upstreamChangeMu.Lock()
	defer s.upstreamChangeMu.Unlock()
	current := s.effectiveUpstream()
	if !sameUpstreamRoute(current, expected) {
		return
	}
	byBackend := make(map[string]string, len(current.ModelsByBackend)+1)
	for id, name := range current.ModelsByBackend {
		byBackend[id] = name
	}
	byBackend[backend] = modelName
	current.ModelsByBackend = byBackend
	current.ModelName = modelName
	if s.settings != nil {
		_ = s.settings.SaveUpstream(ctx, current)
	}
	s.applyUpstream(current, backend == "llamacpp")
}
