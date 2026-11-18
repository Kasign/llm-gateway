package server

import (
	"sync"
	"time"
)

const (
	requestTimelineMaxEvents = 32

	timelineStatusMeasured      = "measured"
	timelineStatusNotApplicable = "not_applicable"
	timelineStatusNotReached    = "not_reached"
	timelineStatusUnknown       = "unknown"
)

// requestTimelineEvent 是单个请求内的相对时间点。偏移只在真实事件发生后写入。
type requestTimelineEvent struct {
	Name     string `json:"name"`
	OffsetMS *int64 `json:"offset_ms,omitempty"`
	Status   string `json:"status"`
}

// requestTimeline 使用请求入口的单调时钟记录有限数量的事件。
// 它只保存事件名、相对偏移和测量状态，不接触请求或响应正文。
type requestTimeline struct {
	mu     sync.Mutex
	start  time.Time
	events []requestTimelineEvent
}

var requestTimelineEventNames = map[string]struct{}{
	"request_received": {}, "auth_started": {}, "auth_finished": {},
	"client_rpm_started": {}, "client_rpm_finished": {},
	"body_read_started": {}, "body_read_finished": {},
	"pipeline_started": {}, "prepared": {}, "ready": {}, "warm_finished": {},
	"upstream_started": {}, "provider_queue_wait": {}, "upstream_dispatched": {},
	"upstream_headers": {}, "upstream_first_byte": {},
	"stream_first_any": {}, "stream_first_reasoning": {}, "stream_first_content": {},
	"gateway_first_write": {}, "gateway_last_write": {},
	"pipeline_finished": {}, "handler_returned": {},
}

func newRequestTimeline(start time.Time) *requestTimeline {
	if start.IsZero() {
		start = time.Now()
	}
	return &requestTimeline{start: start, events: make([]requestTimelineEvent, 0, requestTimelineMaxEvents)}
}

func (t *requestTimeline) record(name string, at time.Time) {
	if t == nil {
		return
	}
	if _, ok := requestTimelineEventNames[name]; !ok {
		return
	}
	if at.IsZero() {
		return
	}
	offset := at.Sub(t.start).Milliseconds()
	if offset < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.events) >= requestTimelineMaxEvents {
		return
	}
	value := offset
	t.events = append(t.events, requestTimelineEvent{
		Name: name, OffsetMS: &value, Status: timelineStatusMeasured,
	})
}

func (t *requestTimeline) recordNow(name string) {
	t.record(name, time.Now())
}

func (t *requestTimeline) mark(name, status string) {
	if t == nil {
		return
	}
	if _, ok := requestTimelineEventNames[name]; !ok {
		return
	}
	switch status {
	case timelineStatusNotApplicable, timelineStatusNotReached, timelineStatusUnknown:
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.events) >= requestTimelineMaxEvents {
		return
	}
	t.events = append(t.events, requestTimelineEvent{Name: name, Status: status})
}

func (t *requestTimeline) snapshot() []requestTimelineEvent {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]requestTimelineEvent, len(t.events))
	copy(out, t.events)
	for i := range out {
		if out[i].OffsetMS != nil {
			value := *out[i].OffsetMS
			out[i].OffsetMS = &value
		}
	}
	return out
}
