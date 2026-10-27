package server

import (
	"context"
	"strings"
)

// proxyRequestContext 跟随客户端连接，并应用配置中的整体请求时限。
// 配置为 0 时不新增整体 Deadline；客户端断开和管理端取消仍会中止上游请求。
func (s *Server) proxyRequestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return contextWithOptionalTimeout(parent, s.cfg.Llama.RequestTimeout)
}

func proxyPathIsResponses(path string) bool {
	return strings.Contains(strings.ToLower(path), "/v1/responses")
}
