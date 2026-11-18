// server（request_analysis_persist）：请求结束后把分析结果异步写入数据库。
package server

import "context"

// persistRequestAnalysis 在请求收尾阶段写入分析 JSON，失败只打日志不影响响应。
func (s *Server) persistRequestAnalysis(ctx context.Context, requestID string, in analysisInput) {
	in.RuntimeSnapshot = s.latestRequestRuntimeSnapshot()
	a := buildRequestAnalysis(in)
	s.audit.SaveRequestAnalysis(requestID, encodeRequestAnalysis(a))
}

// persistRequestTimeline 保存未进入分析阶段的请求时间线，不生成空的阶段耗时或吞吐数据。
func (s *Server) persistRequestTimeline(requestID string, events []requestTimelineEvent) {
	if s == nil || s.audit == nil || len(events) == 0 {
		return
	}
	a := RequestAnalysis{TimelineOnly: true, Timeline: events}
	s.audit.SaveRequestAnalysis(requestID, encodeRequestAnalysis(a))
}
