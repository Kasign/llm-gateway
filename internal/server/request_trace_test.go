package server

import (
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/store"
)

func newRequestTraceForTest(cfg *config.Config) *requestTrace {
	registry := live.NewRegistry()
	session := registry.Register(store.RequestLog{
		RequestID: "trace-test",
		Status:    "running",
		StartedAt: time.Now(),
	})
	return &requestTrace{
		s:       &Server{cfg: cfg},
		id:      "trace-test",
		start:   time.Now(),
		session: session,
	}
}

func TestRequestTraceDoesNotStoreStreamContentByDefault(t *testing.T) {
	trace := newRequestTraceForTest(&config.Config{})
	trace.appendStreamEvent("delta", "content", 12, "sensitive preview", "data: sensitive raw")

	events := trace.drainEvents()
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0].Preview != "" || events[0].RawLine != "" {
		t.Fatalf("stream content should be disabled by default: %+v", events[0])
	}
	if events[0].Kind != "delta" || events[0].Field != "content" || events[0].DeltaLen != 12 {
		t.Fatalf("metadata should remain available: %+v", events[0])
	}
}

func TestRequestTraceContentRetentionFlagsAreIndependent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Logging.StoreStreamPreview = true
	trace := newRequestTraceForTest(cfg)
	trace.appendStreamEvent("delta", "content", 6, "preview text", "data: raw text")
	events := trace.drainEvents()
	if len(events) != 1 || events[0].Preview != "preview text" || events[0].RawLine != "" {
		t.Fatalf("preview flag must not enable raw SSE storage: %+v", events)
	}

	cfg = &config.Config{}
	cfg.Logging.StoreStreamRaw = true
	trace = newRequestTraceForTest(cfg)
	trace.appendStreamEvent("delta", "content", 6, "preview text", "data: raw text")
	events = trace.drainEvents()
	if len(events) != 1 || events[0].Preview != "" || events[0].RawLine != "data: raw text" {
		t.Fatalf("raw flag must not enable preview storage: %+v", events)
	}
}

func TestRequestTraceEventContentHasPerRequestByteBudget(t *testing.T) {
	cfg := &config.Config{}
	cfg.Logging.StoreStreamRaw = true
	cfg.Logging.StoreStreamPreview = true
	trace := newRequestTraceForTest(cfg)

	largePreview := strings.Repeat("p", maxStreamEventContentBytesPerRequest)
	largeRaw := strings.Repeat("r", maxStreamEventRawBytes+10)
	trace.appendStreamEvent("delta", "content", len(largePreview), largePreview, largeRaw)
	trace.appendStreamEvent("delta", "content", 1, "later preview", "later raw")

	events := trace.drainEvents()
	if len(events) != 2 {
		t.Fatalf("event metadata should be retained within the event-count limit, got %d", len(events))
	}
	if got := len(events[0].Preview) + len(events[0].RawLine); got > maxStreamEventContentBytesPerRequest {
		t.Fatalf("per-request content budget exceeded: %d", got)
	}
	if trace.contentBytes > maxStreamEventContentBytesPerRequest {
		t.Fatalf("tracked content budget exceeded: %d", trace.contentBytes)
	}
	if len(events[0].RawLine) > maxStreamEventRawBytes {
		t.Fatalf("raw line limit exceeded: %d", len(events[0].RawLine))
	}
	if events[1].Preview != "" || events[1].RawLine != "" {
		t.Fatalf("content must be omitted after the request budget is exhausted: %+v", events[1])
	}
}

func TestRequestTraceCapsUnterminatedParserLine(t *testing.T) {
	trace := newRequestTraceForTest(&config.Config{})
	trace.recordStreamChunk([]byte(strings.Repeat("x", maxStreamParserLineBytes+1)))
	if trace.lineCarry != "" {
		t.Fatalf("oversized unterminated SSE line should be discarded, got %d bytes", len(trace.lineCarry))
	}
}

func TestLivePayloadIsNotRetainedWhenDisabled(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{cfg: cfg, live: live.NewRegistry()}
	const id = "payload-disabled-test"
	s.liveStart(nil, store.RequestLog{RequestID: id, Status: "running", StartedAt: time.Now()})
	s.liveSetReqBody(id, []byte(`{"prompt":"private"}`))
	s.liveSetResBody(id, []byte(`{"answer":"private"}`))
	snap := s.live.SnapshotDetail(id)
	if snap == nil {
		t.Fatal("expected live session")
	}
	if len(snap.ReqBody) != 0 || len(snap.ResBody) != 0 {
		t.Fatalf("payload leaked into live session when disabled: request=%d response=%d", len(snap.ReqBody), len(snap.ResBody))
	}
}

func TestRequestTraceDoesNotCountRoleMetadataAsTTFT(t *testing.T) {
	trace := newRequestTraceForTest(&config.Config{})
	trace.recordStreamChunk([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	if trace.streamTimings().FirstAnyMS != nil {
		t.Fatal("role metadata must not count as first output")
	}
	trace.recordStreamChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	timings := trace.streamTimings()
	if timings.FirstAnyMS == nil || timings.FirstContentMS == nil {
		t.Fatalf("actual content should set TTFT and first-content times: %+v", timings)
	}
	if !trace.ttftRecorded {
		t.Fatal("TTFT should be recorded after actual output")
	}
}
