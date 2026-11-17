package server

import (
	"context"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
)

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
