package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/inferenceparams"
)

// applyInferenceParamsToBody 是主流程 Mutate 回调用的薄封装。
// 字段如何合并在 inferenceparams 里完成，这里只取出当前上游的策略和配置，并写回完整请求体。
func (s *Server) applyInferenceParamsToBody(ctx context.Context, path string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	pl := strings.ToLower(path)
	if !(strings.Contains(pl, "/v1/chat/completions") || strings.Contains(pl, "/v1/messages") || strings.Contains(pl, "/v1/responses")) {
		return body
	}
	be := s.effectiveUpstream().Backend
	params, policy, err := s.inferenceParamsStore().LoadEffective(ctx, be)
	if err != nil {
		return body
	}
	mode := policy.Mode
	if mode == "" {
		mode = inferenceparams.ModeClientFirst
	}
	return inferenceparams.MergeChatBody(be, mode, params, body)
}

func (s *Server) ollamaKeepAlive(ctx context.Context) string {
	const fallback = "24h"
	if s == nil || s.settings == nil {
		return fallback
	}
	params, _, err := s.inferenceParamsStore().LoadEffective(ctx, "ollama")
	if err != nil || params.KeepAlive == nil {
		return fallback
	}
	if v := strings.TrimSpace(*params.KeepAlive); v != "" {
		return v
	}
	return fallback
}
