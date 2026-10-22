// server（request_analysis_persist）：请求结束后把分析结果异步写入数据库。
package server

import "context"

// persistRequestAnalysis 在请求收尾阶段写入分析 JSON，失败只打日志不影响响应。
func (s *Server) persistRequestAnalysis(ctx context.Context, requestID string, in analysisInput) {
	a := buildRequestAnalysis(in)
	s.audit.SaveRequestAnalysis(requestID, encodeRequestAnalysis(a))
}
