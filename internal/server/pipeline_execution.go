package server

import (
	"context"
	"errors"
	"strconv"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
)

// allowClientKeyRequest 统一普通 API 与 Benchmark 的客户端 Key RPM 计数。
// Provider 自身的 RPM 是 Pipeline Guard，使用独立计数桶，不与客户端 Key RPM 混用。
func (s *Server) allowClientKeyRequest(ctx context.Context, key *auth.Key) (bool, error) {
	if s == nil || s.limiter == nil {
		return false, errors.New("rate limiter unavailable")
	}
	if key == nil {
		return false, errors.New("client API key is unavailable")
	}
	allowed, _, err := s.limiter.Allow(ctx, strconv.FormatUint(key.ID, 10), key.RPM)
	return allowed, err
}

// inferenceRequestGuards 为外部 API 与服务端测试任务提供相同的调用方模型 ACL、Provider 限流检查。
// 全局 Guard 仍由 newInferencePipeline 注册；这里保持原有请求级 Guard 顺序。
func (s *Server) inferenceRequestGuards(key *auth.Key) []core.Guard {
	return []core.Guard{
		s.allowModelsGuard(key),
		s.providerRateLimitGuard(),
	}
}

// beginInferenceRun 统一登记一次 Pipeline Run 的请求状态与取消入口。
// 调用方应在原有登记位置调用，并 defer 返回的清理函数，以保持各入口现有时序。
func (s *Server) beginInferenceRun(id string, key *auth.Key, cancel context.CancelFunc, registerCallCancel bool) func() {
	if s.runs != nil {
		s.runs.Register(id, cancel)
	}
	s.callBegin(id, key)
	if registerCallCancel {
		s.setCallCancel(id, cancel)
	}
	return func() {
		s.callDrop(id)
		if s.runs != nil {
			s.runs.Unregister(id)
		}
	}
}
