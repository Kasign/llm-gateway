package server

import (
	"context"
	"strings"
	"time"
)

func (s *Server) llamaRequestTimeout() time.Duration {
	sec := s.cfg.Llama.RequestTimeout
	if sec <= 0 {
		sec = 3600
	}
	return time.Duration(sec) * time.Second
}

// proxyRequestContext 跟随客户端连接，并受网关请求超时限制。
// 客户端断开、管理端结束、超时都会取消这次上游请求。
func (s *Server) proxyRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.llamaRequestTimeout())
}

func proxyPathIsResponses(path string) bool {
	return strings.Contains(strings.ToLower(path), "/v1/responses")
}
