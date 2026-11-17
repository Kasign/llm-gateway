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

// runInferencePipeline 是 HTTP 请求与服务端任务共用的执行边界。
// HTTP 层仍负责认证、RPM、Body 限制、Context 和响应协议；调用方负责构造各自的 Sink/Request。
// 此方法只统一 Run 周围的请求状态与取消登记，不改变 core.Pipeline 阶段或请求语义。
func (s *Server) runInferencePipeline(
	ctx context.Context,
	req core.Request,
	key *auth.Key,
	cancel context.CancelFunc,
	registerCallCancel bool,
) (core.Result, error) {
	if s.runs != nil {
		s.runs.Register(req.ID, cancel)
		defer s.runs.Unregister(req.ID)
	}
	s.callBegin(req.ID, key)
	if registerCallCancel {
		s.setCallCancel(req.ID, cancel)
	}
	defer s.callDrop(req.ID)

	return s.pipe.Run(ctx, req)
}
