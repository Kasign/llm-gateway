package server

import (
	"context"
	"strings"

	"github.com/whl/llm-gateway/internal/inferenceparams"
)

// applyInferenceParamsToBody 是主流程 Mutate 回调用的薄封装。
// 字段如何合并在 inferenceparams 里完成，这里只取出当前上游的策略和配置，并写回完整请求体。
func (s *Server) applyInferenceParamsToBody(ctx context.Context, path string, body []byte) []byte {
	// 保留旧内部调用契约；主推理流程调用显式带 Provider ID 的版本。
	return s.applyInferenceParamsToBodyForBackend(ctx, s.effectiveUpstream().Backend, path, body)
}

// applyInferenceParamsToBodyForBackend 严格使用本次请求选定的后端加载对应参数策略。
func (s *Server) applyInferenceParamsToBodyForBackend(ctx context.Context, backend, path string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	// 当前参数面板只为 llama.cpp/Ollama 定义默认值与 thinking 参数；不可把本地默认采样字段注入远程请求。
	if !isLocalInferenceBackend(backend) {
		return body
	}
	pl := strings.ToLower(path)
	if !(strings.Contains(pl, "/v1/chat/completions") || strings.Contains(pl, "/v1/messages") || strings.Contains(pl, "/v1/responses")) {
		return body
	}
	params, policy, err := s.inferenceParamsStore().LoadEffective(ctx, backend)
	if err != nil {
		return body
	}
	mode := policy.Mode
	if mode == "" {
		mode = inferenceparams.ModeClientFirst
	}
	return inferenceparams.MergeChatBody(backend, mode, params, body)
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
