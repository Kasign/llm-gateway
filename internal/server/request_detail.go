// server（request_detail）：拼装单条请求的管理台详情（摘要、诊断、可选正文与流事件）。
package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/store"
)

func buildRequestDetail(l *store.RequestLog, payload map[string]string, storePayloadEnabled bool, streamEvents []store.StreamEvent, analysisRaw string) map[string]interface{} {
	summary := buildRequestSummary(l)
	effective := effectiveStream(l, len(streamEvents))
	summary["stream_effective"] = effective
	if effective && !l.Stream {
		summary["stream_note"] = "库中 stream=0（历史 bug 或未写入），根据 TTFT/流式事件推断为流式"
	}
	timing := buildRequestTiming(l)
	out := map[string]interface{}{
		"log":       l,
		"summary":   summary,
		"timing":    timing,
		"diagnosis": buildRequestDiagnosis(l),
	}
	if a, err := decodeRequestAnalysis(analysisRaw); err == nil && a != nil {
		out["analysis"] = a
		if src := strings.TrimSpace(a.Throughput.Source); src != "" {
			timing["token_source"] = src
			timing["token_source_label"] = tokenSourceLabel(src)
		}
	} else if rebuilt := rebuildAnalysisFromLog(l, streamEvents); rebuilt != nil {
		out["analysis"] = rebuilt
		out["analysis_note"] = "由日志字段与 stream_events 推算（未持久化 analysis_json 或旧记录）"
	}
	if len(streamEvents) > 0 {
		out["stream_events"] = streamEvents
		out["stream_event_count"] = len(streamEvents)
	}
	var reqBody []byte
	if payload != nil {
		out["payload"] = payload
		req := payload["request_body"]
		res := payload["response_body"]
		reqBody = []byte(req)
		if req != "" {
			out["request_body"] = req
			out["request_body_bytes"] = len(req)
			out["request_body_note"] = "Gateway 转发给上游的 HTTP body（已替换 model；流式时可能注入 stream_options.include_usage；system 合并见 sanitize 规则；tools 不改写）"
			out["request_preview"] = truncateForPreview(req, 12000)
		}
		if res != "" {
			out["response_body"] = res
			out["response_body_bytes"] = len(res)
			out["response_preview"] = truncateForPreview(res, 12000)
			attachAggregatedText(out, []byte(res))
			// 旧记录库里 token 为空时，打开详情按当前规则再解析，不回写数据库。
			if l != nil && l.InputTokens == nil && l.OutputTokens == nil && l.TotalTokens == nil {
				meta := logutil.TokensFromResponse([]byte(res))
				if !meta.Empty() {
					applyHarvestToTiming(timing, meta)
					out["token_note"] = "由响应体按当前规则重新解析（库中记录为空，未回写）"
				}
			}
		}
		attachDetailHeaders(out, payload)
	} else if storePayloadEnabled {
		out["payload_note"] = "未保存 payload（可能请求进行中、写入失败，或响应超过 8MB 截断策略）"
		out["headers_note"] = "本次未保存请求头"
	} else {
		out["payload_note"] = "logging.store_payload=false，未保存完整请求/响应 body；请在 config.yaml 设置 logging.store_payload: true 后对新请求生效。流式仍可通过 stream_events.raw_line 查看各 SSE 行。"
		out["headers_note"] = "本次未保存请求头"
	}
	out["request_params"] = buildRequestParams(l, reqBody)
	content, reasoning := aggregateTextFromStreamEvents(streamEvents)
	if content != "" {
		out["content_text"] = content
	}
	if reasoning != "" {
		out["reasoning_text"] = reasoning
	}
	if len(streamEvents) > 0 {
		out["stream_events_note"] = "每条流式 SSE 对应 stream_events 一行；raw_line 为 data: 后的 JSON（或 [DONE]）。若已开启 store_payload，完整 SSE 另见 response_body。"
	}
	return out
}

func attachDetailHeaders(out map[string]interface{}, payload map[string]string) {
	client := parseHeadersJSON(payload["request_headers"])
	upstream := parseHeadersJSON(payload["upstream_headers"])
	if len(client) == 0 && len(upstream) == 0 {
		out["headers_note"] = "本次未保存请求头（旧记录）"
		return
	}
	if len(client) > 0 {
		out["request_headers"] = client
	}
	if len(upstream) > 0 {
		out["upstream_headers"] = upstream
	}
}

func applyHarvestToTiming(timing map[string]interface{}, meta logutil.TokenHarvest) {
	if timing == nil {
		return
	}
	if meta.InputTokens != nil {
		timing["input_tokens"] = *meta.InputTokens
	}
	if meta.OutputTokens != nil {
		timing["output_tokens"] = *meta.OutputTokens
	}
	if meta.TotalTokens != nil {
		timing["total_tokens"] = *meta.TotalTokens
	}
	if meta.Source != "" {
		timing["token_source"] = meta.Source
		timing["token_source_label"] = tokenSourceLabel(meta.Source)
	}
}

func attachAggregatedText(out map[string]interface{}, body []byte) {
	if logutil.LooksLikeSSE(body) {
		content, reasoning := logutil.AggregateChatStream(body)
		if content != "" {
			out["content_text"] = content
		}
		if reasoning != "" {
			out["reasoning_text"] = reasoning
		}
		return
	}
	var obj map[string]interface{}
	if json.Unmarshal(body, &obj) != nil {
		return
	}
	if choices, ok := obj["choices"].([]interface{}); ok && len(choices) > 0 {
		if m, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := m["message"].(map[string]interface{}); ok {
				if t := textFromValue(msg["content"]); t != "" {
					out["content_text"] = t
				}
			}
		}
	}
}

func aggregateTextFromStreamEvents(events []store.StreamEvent) (content, reasoning string) {
	for _, e := range events {
		raw := e.RawLine
		if raw == "" {
			raw = e.Preview
		}
		if raw == "" || raw == "[DONE]" {
			continue
		}
		d, ok := logutil.ParseStreamDelta(raw)
		if !ok {
			continue
		}
		content += d.Content
		reasoning += d.Reasoning
	}
	return content, reasoning
}

func effectiveStream(l *store.RequestLog, streamEventCount int) bool {
	if l == nil {
		return false
	}
	if l.Stream {
		return true
	}
	if streamEventCount > 0 {
		return true
	}
	if l.TTFTMS != nil && *l.TTFTMS > 0 {
		return true
	}
	return false
}

func buildRequestSummary(l *store.RequestLog) map[string]interface{} {
	s := map[string]interface{}{
		"request_id":  l.RequestID,
		"status":      l.Status,
		"http_status": l.HTTPStatus,
		"method":      l.Method,
		"endpoint":    l.Endpoint,
		"request_url": l.RequestURL,
		"stream":      l.Stream,
		"client_ip":   l.ClientIP,
		"client_name": l.ClientName,
		"user_agent":  l.UserAgent,
		"api_key":     l.APIKeyName,
		"api_key_id":  l.APIKeyID,
	}
	if l.ModelRequested != "" {
		s["model_client"] = l.ModelRequested
	}
	if l.ModelActual != "" {
		s["model_upstream"] = l.ModelActual
	}
	if l.ModelRequested != "" && l.ModelActual != "" && l.ModelRequested != l.ModelActual {
		s["model_note"] = "Gateway 已忽略客户端 model，上游实际使用 model_upstream"
	}
	s["request_kb"] = roundKB(l.RequestBytes)
	s["response_kb"] = roundKB(l.ResponseBytes)
	if l.ErrorCode != "" {
		s["error_code"] = l.ErrorCode
	}
	if l.ErrorMessage != "" {
		s["error_message"] = l.ErrorMessage
	}
	return s
}

func buildRequestTiming(l *store.RequestLog) map[string]interface{} {
	t := map[string]interface{}{
		"started_at":    l.StartedAt,
		"latency_ms":    l.LatencyMS,
		"latency_human": fmtDurationMS(l.LatencyMS),
	}
	if l.FinishedAt != nil {
		t["finished_at"] = l.FinishedAt
	}
	if l.TTFTMS != nil {
		t["ttft_ms"] = *l.TTFTMS
		t["ttft_human"] = fmtDurationMS(*l.TTFTMS)
		if l.LatencyMS > *l.TTFTMS {
			gen := l.LatencyMS - *l.TTFTMS
			t["generation_ms"] = gen
			t["generation_human"] = fmtDurationMS(gen)
		}
	}
	if l.InputTokens != nil {
		t["input_tokens"] = *l.InputTokens
	}
	if l.OutputTokens != nil {
		t["output_tokens"] = *l.OutputTokens
	}
	if l.TotalTokens != nil {
		t["total_tokens"] = *l.TotalTokens
	}
	return t
}

func buildRequestDiagnosis(l *store.RequestLog) []string {
	var hints []string
	msg := strings.ToLower(l.ErrorMessage)
	lat := l.LatencyMS

	if strings.Contains(msg, "客户端连接已断开") || strings.Contains(msg, "保活写出失败") || strings.Contains(msg, "写回客户端失败") {
		hints = append(hints, "客户端连接已结束，这次上游请求已取消。error_message 是具体原因。")
		if lat >= 55000 && lat <= 65000 {
			hints = append(hints, "耗时约 60s，高度疑似 Codex/HTTP 客户端 60 秒超时。")
		}
		if lat >= 290000 && lat <= 310000 {
			hints = append(hints, "耗时约 300s，疑似客户端 5 分钟超时。")
		}
	}
	if l.ErrorCode == "gateway_timeout" || strings.Contains(msg, "网关请求超时") || strings.Contains(msg, "context deadline exceeded") {
		hints = append(hints, "达到网关 request_timeout_seconds，上游请求已取消。")
	}
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded") {
		hints = append(hints, "客户端或中间代理提前断开（context canceled）。常见：桌面端约 60s 超时，而首 token 尚未返回。")
		if lat >= 55000 && lat <= 65000 {
			hints = append(hints, "耗时约 60s，高度疑似 Codex/HTTP 客户端 60 秒超时。")
		}
		if lat >= 290000 && lat <= 310000 {
			hints = append(hints, "耗时约 300s，疑似客户端 5 分钟超时。")
		}
	}
	if l.ErrorCode == "upstream_http_error" && l.HTTPStatus == 503 {
		hints = append(hints, "上游 llama 返回 503，多为模型仍在 loading；可预加载或等待 Gateway load 完成。")
	}
	if l.ErrorCode == "model_load_failed" {
		hints = append(hints, "Gateway 在出网转发前被 auto_load 拦截，llama.cpp 通常不会出现该次 chat/responses 日志；Benchmark 会单独 load 故可能仍正常。检查当前生效上游 backend、模型名与 router。")
	}
	switch l.ErrorCode {
	case "no_proxy_model", "upstream_model_mismatch", "model_disabled":
		hints = append(hints, "未进入上游 HTTP：在 resolveProxyModelTarget 阶段失败；llama.cpp 无对应访问日志。")
	case "invalid_api_key", "rate_limit_exceeded", "rate_limit_unavailable":
		hints = append(hints, "鉴权/限流阶段拒绝，请求未转发到 llama.cpp。")
	case "method_not_allowed":
		hints = append(hints, "非 POST（例如 WebSocket 握手 GET）会在 Gateway 直接 405，不会到 llama.cpp。")
	}
	if l.Endpoint == "/v1/models" {
		hints = append(hints, "GET /v1/models 由 Gateway 读数据库组装，不会访问 llama.cpp。")
	}
	if l.Status == "running" && l.Stream && l.ResponseBytes == 0 && l.RequestBytes > 32*1024 {
		hints = append(hints, "流式请求已转发但尚无 SSE：大上下文 prefill 可能需数分钟；Codex 带 tools 的 Responses 体在旧版 Gateway 可能导致 llama 长时间无首 token。")
	}
	if strings.Contains(strings.ToLower(l.Endpoint), "responses") && l.RequestBytes > 0 {
		hints = append(hints, "/v1/responses 原样转发，tools 不改写；详情 request_body 为实际上游 JSON。不支持的 tool 类型由上游处理。")
	}
	if !l.Stream && l.RequestBytes > 100*1024 && l.TTFTMS != nil && *l.TTFTMS > 45000 {
		hints = append(hints, "非流式 + 大请求体：prefill 耗时长，首字节前客户端易超时；建议流式或缩短上下文。")
	}
	if l.Status == "success" && l.OutputTokens != nil && *l.OutputTokens == 0 && l.ResponseBytes < 64 {
		hints = append(hints, "HTTP 成功但几乎无输出，检查上游 body 是否为空或被截断。")
	}
	if l.Stream && l.Status == "success" {
		hints = append(hints, "流式请求：详情中的 stream_events 列出各 chunk；若仅有 reasoning 无 content，多为思考链占满输出或未结束。")
	}
	if len(hints) == 0 && l.Status == "error" && l.ErrorMessage == "" {
		hints = append(hints, "无 error_message 文本；多为历史记录或未写入详细错误，新请求会带更完整信息。")
	}
	return hints
}

func truncateForPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…（已截断，共 %d 字符）", len(s))
}

func fmtDurationMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%.1f min", d.Minutes())
}

func roundKB(n int64) float64 {
	if n <= 0 {
		return 0
	}
	return float64(n) / 1024
}

func upstreamErrorMessage(status int, body []byte, err error) string {
	if err != nil {
		return err.Error()
	}
	msg := fmt.Sprintf("upstream HTTP %d", status)
	sample := strings.TrimSpace(string(body))
	if sample != "" {
		if len(sample) > 600 {
			sample = sample[:600] + "…"
		}
		msg += ": " + sample
	}
	return msg
}
