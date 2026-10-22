// server（request_live）：进程内 LiveRegistry，供管理台查看进行中的完整调用记录。
package server

import (
	"context"
	"sort"
	"strings"

	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/store"
)

func (s *Server) liveStart(_ context.Context, l store.RequestLog) {
	if s.live == nil {
		return
	}
	s.live.Register(l)
}

func (s *Server) livePatch(_ context.Context, id string, patch store.RequestLog) {
	if sess := s.liveSession(id); sess != nil {
		sess.PatchLog(patch)
	}
}

func (s *Server) liveSetHeaders(id string, client, upstream map[string]string) {
	if sess := s.liveSession(id); sess != nil {
		sess.SetHeaders(client, upstream)
	}
}

func (s *Server) liveSetReqBody(id string, body []byte) {
	// 正文留存关闭时，实时看板也不保留请求正文，避免配置语义前后不一致。
	if s == nil || s.cfg == nil || !s.cfg.Logging.StorePayload {
		return
	}
	if sess := s.liveSession(id); sess != nil && len(body) > 0 {
		sess.SetReqBody(body)
	}
}

func (s *Server) liveSetResBody(id string, body []byte) {
	// 与请求正文保持一致：关闭 Payload 留存时不在实时看板缓存完整响应正文。
	if s == nil || s.cfg == nil || !s.cfg.Logging.StorePayload {
		return
	}
	if sess := s.liveSession(id); sess != nil && len(body) > 0 {
		sess.SetResBody(body)
	}
}

func (s *Server) liveAppendResBody(id string, chunk []byte) {
	if sess := s.liveSession(id); sess != nil {
		sess.AppendResBody(chunk, s.cfg.Logging.StorePayload)
	}
}

func (s *Server) liveSyncStream(id string, trace *requestTrace, bytesN int64, meta *logutil.TokenHarvest) {
	sess := s.liveSession(id)
	if sess == nil {
		return
	}
	var ttft *int64
	if trace != nil {
		st := trace.streamTimings()
		ttft = st.FirstAnyMS
	}
	var in, out, total *int64
	if meta != nil {
		in, out, total = meta.InputTokens, meta.OutputTokens, meta.TotalTokens
	}
	sess.UpdateStreamScalars(bytesN, ttft, in, out, total)
}

func (s *Server) liveFinish(_ context.Context, id string) {
	if s.live == nil {
		return
	}
	s.live.Unregister(id)
}

func (s *Server) liveSession(id string) *live.Session {
	if s.live == nil || id == "" {
		return nil
	}
	return s.live.Get(id)
}

func mergeLiveIntoLog(base store.RequestLog, live store.RequestLog) store.RequestLog {
	out := base
	if live.APIKeyName != "" {
		out.APIKeyName = live.APIKeyName
	}
	if live.APIKeyID != 0 {
		out.APIKeyID = live.APIKeyID
	}
	if live.ModelRequested != "" {
		out.ModelRequested = live.ModelRequested
	}
	if live.ModelActual != "" {
		out.ModelActual = live.ModelActual
	}
	if live.RequestURL != "" {
		out.RequestURL = live.RequestURL
	}
	if live.Stream {
		out.Stream = true
	}
	if live.RequestBytes > 0 {
		out.RequestBytes = live.RequestBytes
	}
	if live.ResponseBytes > 0 {
		out.ResponseBytes = live.ResponseBytes
	}
	if live.TTFTMS != nil {
		out.TTFTMS = live.TTFTMS
	}
	if live.InputTokens != nil {
		out.InputTokens = live.InputTokens
	}
	if live.OutputTokens != nil {
		out.OutputTokens = live.OutputTokens
	}
	if live.TotalTokens != nil {
		out.TotalTokens = live.TotalTokens
	}
	if live.Status == "running" || out.Status == "running" {
		out.Status = "running"
		out.LatencyMS = live.LatencyMS
	}
	if out.ClientName == "" {
		out.ClientName = live.ClientName
	}
	if out.UserAgent == "" {
		out.UserAgent = live.UserAgent
	}
	return out
}

func fillClientNames(logs []store.RequestLog) {
	for i := range logs {
		logs[i].ClientName = displayClientName(logs[i].ClientName, logs[i].UserAgent)
	}
}

func (s *Server) queryRequestLogsMerged(ctx context.Context, limit int, requestID, modelName, status, keyName string) ([]store.RequestLog, error) {
	logs, err := s.queryRequestLogsCached(ctx, limit, requestID, modelName, status, keyName)
	if err != nil {
		return nil, err
	}
	if s.live == nil || requestID != "" {
		fillClientNames(logs)
		return logs, nil
	}
	liveRows := s.live.ListSummaries()
	if len(liveRows) == 0 {
		fillClientNames(logs)
		return logs, nil
	}
	byID := make(map[string]int, len(logs))
	for i, l := range logs {
		byID[l.RequestID] = i
	}
	for _, row := range liveRows {
		if status != "" && status != "running" {
			continue
		}
		if modelName != "" && !logMatchesModelFilter(row, modelName) {
			continue
		}
		if keyName != "" && !strings.Contains(strings.ToLower(row.APIKeyName), strings.ToLower(keyName)) {
			continue
		}
		if idx, ok := byID[row.RequestID]; ok {
			logs[idx] = mergeLiveIntoLog(logs[idx], row)
		} else {
			logs = append(logs, row)
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		ti, tj := logs[i].StartedAt, logs[j].StartedAt
		if ti.IsZero() && tj.IsZero() {
			return logs[i].RequestID > logs[j].RequestID
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		if ti.Equal(tj) {
			return logs[i].RequestID > logs[j].RequestID
		}
		return ti.After(tj)
	})
	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}
	fillClientNames(logs)
	return logs, nil
}

func logMatchesModelFilter(l store.RequestLog, modelName string) bool {
	needle := strings.ToLower(modelName)
	return strings.Contains(strings.ToLower(l.ModelRequested), needle) ||
		strings.Contains(strings.ToLower(l.ModelActual), needle)
}

func livePayloadFromSnapshot(snap live.DetailSnapshot) map[string]string {
	out := map[string]string{}
	if len(snap.ReqBody) > 0 {
		out["request_body"] = string(snap.ReqBody)
	}
	if len(snap.ResBody) > 0 {
		out["response_body"] = string(snap.ResBody)
	}
	if len(snap.ClientHeaders) > 0 {
		out["request_headers"] = headersJSON(snap.ClientHeaders)
	}
	if len(snap.UpstreamHeaders) > 0 {
		out["upstream_headers"] = headersJSON(snap.UpstreamHeaders)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
