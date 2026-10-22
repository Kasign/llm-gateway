// server（request_trace）：流式代理过程中记录 SSE 片段与首 token 时间，写入 stream_events 表。
package server

import (
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/store"
)

const maxStreamEventsPerRequest = 2000
const maxStreamEventRawBytes = 65535

// requestTrace 绑定一条 request_id；流式片段只缓存在内存，请求结束由 audit 批量异步入库。
type requestTrace struct {
	s            *Server
	id           string
	start        time.Time
	stream       bool
	seq          int
	ttftRecorded bool
	lineCarry    string
	timing       streamTimings
	session      *live.Session
}

func (s *Server) attachRequestTrace(id string, start time.Time) *requestTrace {
	return &requestTrace{s: s, id: id, start: start, stream: true, session: s.liveSession(id)}
}

// StreamEventCount 返回已记录的流式事件条数（内存计数，不查库）。
func (t *requestTrace) StreamEventCount() int {
	return t.seq
}

func (t *requestTrace) drainEvents() []store.StreamEvent {
	if t.session != nil {
		return t.session.StreamEventsCopy()
	}
	return nil
}

func (t *requestTrace) recordStreamChunk(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	logutil.ForEachStreamDataLine(chunk, &t.lineCarry, func(raw string) {
		if raw == "[DONE]" {
			t.appendStreamEvent("done", "", 0, raw, raw)
			applog.Debugf("[%s] stream event: done", t.id)
			return
		}
		if in, out, total, ok := logutil.ParseUsageChunk([]byte("data: " + raw)); ok {
			t.appendStreamEvent("usage", "", 0, previewText(raw, 160), raw)
			applog.Debugf("[%s] stream usage: in=%d out=%d total=%d", t.id, in, out, total)
			return
		}
		d, ok := logutil.ParseStreamDelta(raw)
		if !ok {
			if strings.TrimSpace(raw) != "" {
				t.appendStreamEvent("sse", "", len(raw), previewText(raw, 160), raw)
				applog.Debugf("[%s] stream sse unparsed +%d bytes", t.id, len(raw))
			}
			return
		}
		at := time.Since(t.start).Milliseconds()
		if at > t.timing.LastEventMS {
			t.timing.LastEventMS = at
		}
		if d.Reasoning != "" {
			if t.timing.FirstReasoningMS == nil {
				v := at
				t.timing.FirstReasoningMS = &v
			}
		}
		if d.Content != "" {
			if t.timing.FirstContentMS == nil {
				v := at
				t.timing.FirstContentMS = &v
			}
		}
		if !t.ttftRecorded && (d.Content != "" || d.Reasoning != "" || d.Role != "") {
			t.ttftRecorded = true
			v := at
			t.timing.FirstAnyMS = &v
			t.appendStreamEvent("ttft", "", 0, previewText(raw, 160), raw)
			applog.Infof("[%s] stream ttft=%dms", t.id, at)
		}
		if d.Role != "" && d.Content == "" && d.Reasoning == "" && d.Finish == "" {
			t.appendStreamEvent("delta", "role", len(d.Role), previewText(d.Role, 160), raw)
		}
		if d.Reasoning != "" {
			t.appendStreamEvent("delta", "reasoning", len(d.Reasoning), previewText(d.Reasoning, 160), raw)
			applog.Debugf("[%s] stream delta reasoning +%d chars", t.id, len(d.Reasoning))
		}
		if d.Content != "" {
			t.appendStreamEvent("delta", "content", len(d.Content), previewText(d.Content, 160), raw)
			applog.Debugf("[%s] stream delta content +%d chars", t.id, len(d.Content))
		}
		if d.Finish != "" {
			t.appendStreamEvent("finish", d.Finish, 0, previewText(raw, 160), raw)
			applog.Infof("[%s] stream finish_reason=%s", t.id, d.Finish)
		}
	})
}

// recordKeepalive 记下网关自己写出的保活注释。每次轮询各记一条，不影响 TTFT 等时间统计。
func (t *requestTrace) recordKeepalive(n int, raw string) {
	if n <= 0 {
		return
	}
	t.appendStreamEvent("keepalive", "", n, "gateway keepalive", raw)
}

func (t *requestTrace) appendStreamEvent(kind, field string, deltaLen int, preview, rawLine string) {
	if t.seq >= maxStreamEventsPerRequest {
		return
	}
	t.seq++
	rawStored := ""
	if strings.TrimSpace(rawLine) != "" {
		rawStored = truncateStreamRaw(rawLine)
	}
	ev := store.StreamEvent{
		Seq:      t.seq,
		AtMS:     time.Since(t.start).Milliseconds(),
		Kind:     kind,
		Field:    field,
		DeltaLen: deltaLen,
		Preview:  preview,
		RawLine:  rawStored,
	}
	if t.session != nil {
		t.session.AppendStreamEvent(ev)
	}
}

func (t *requestTrace) streamTimings() streamTimings {
	return t.timing
}

func previewText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func truncateStreamRaw(s string) string {
	if len(s) <= maxStreamEventRawBytes {
		return s
	}
	return s[:maxStreamEventRawBytes] + "…"
}
