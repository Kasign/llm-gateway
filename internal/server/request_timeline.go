package server

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/core"
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
	analysis *analysisInput
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
	"pipeline_finished": {}, "handler_exit_started": {},
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


func (t *requestTimeline) recordFirst(name string, at time.Time) {
	if t == nil || at.IsZero() {
		return
	}
	if _, ok := requestTimelineEventNames[name]; !ok {
		return
	}
	offset := at.Sub(t.start).Milliseconds()
	if offset < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, event := range t.events {
		if event.Name == name {
			return
		}
	}
	if len(t.events) >= requestTimelineMaxEvents {
		return
	}
	value := offset
	t.events = append(t.events, requestTimelineEvent{Name: name, OffsetMS: &value, Status: timelineStatusMeasured})
}

func (t *requestTimeline) recordLatest(name string, at time.Time) {
	if t == nil || at.IsZero() {
		return
	}
	if _, ok := requestTimelineEventNames[name]; !ok {
		return
	}
	offset := at.Sub(t.start).Milliseconds()
	if offset < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	value := offset
	for i := range t.events {
		if t.events[i].Name == name {
			t.events[i].OffsetMS = &value
			t.events[i].Status = timelineStatusMeasured
			return
		}
	}
	if len(t.events) >= requestTimelineMaxEvents {
		return
	}
	t.events = append(t.events, requestTimelineEvent{Name: name, OffsetMS: &value, Status: timelineStatusMeasured})
}

func (t *requestTimeline) recordNow(name string) {
	t.record(name, time.Now())
}

func (t *requestTimeline) markIfMissing(name, status string) {
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
	for _, event := range t.events {
		if event.Name == name {
			return
		}
	}
	if len(t.events) >= requestTimelineMaxEvents {
		return
	}
	t.events = append(t.events, requestTimelineEvent{Name: name, Status: status})
}

func (t *requestTimeline) setAnalysis(in analysisInput) {
	if t == nil { return }
	t.mu.Lock()
	defer t.mu.Unlock()
	copy := in
	t.analysis = &copy
}

func (t *requestTimeline) analysisSnapshot() (analysisInput, bool) {
	if t == nil { return analysisInput{}, false }
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.analysis == nil { return analysisInput{}, false }
	out := *t.analysis
	return out, true
}

func (t *requestTimeline) has(name string) bool {
	if t == nil { return false }
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, event := range t.events {
		if event.Name == name && event.Status == timelineStatusMeasured { return true }
	}
	return false
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OffsetMS == nil { return false }
		if out[j].OffsetMS == nil { return true }
		return *out[i].OffsetMS < *out[j].OffsetMS
	})
	return out
}

type requestTimelineContextKey struct{}

func requestTimelineFromContext(ctx context.Context) *requestTimeline {
	if ctx == nil {
		return nil
	}
	timeline, _ := ctx.Value(requestTimelineContextKey{}).(*requestTimeline)
	return timeline
}

// On 将 Pipeline 的既有里程碑映射到请求时间线，不参与决策或改变执行顺序。
func (t *requestTimeline) On(_ context.Context, ev core.Event) {
	if t == nil {
		return
	}
	switch ev.Stage {
	case core.StageParsed:
		t.recordFirst("pipeline_started", ev.State.ParsedAt)
	case core.StagePrepared:
		t.recordFirst("prepared", ev.State.PreparedAt)
	case core.StageReady:
		t.recordFirst("ready", ev.State.ReadyAt)
	case core.StageWarmStart:
		t.recordFirst("warm_finished", ev.State.WarmAt)
	case core.StageUpstreamStart:
		t.recordFirst("upstream_started", ev.State.UpstreamStart)
	}
}
