package server

import (
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

func TestBuildRequestDetailAlwaysIncludesToolCalls(t *testing.T) {
	log := &store.RequestLog{
		RequestID: "req_tool_audit",
		Status: "success",
		HTTPStatus: 200,
		Method: "POST",
		Endpoint: "/v1/chat/completions",
		StartedAt: time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC),
	}
	empty := buildRequestDetail(log, nil, false, nil, "")
	if events, ok := empty["tool_calls"].([]store.ToolCallEvent); !ok || len(events) != 0 {
		t.Fatalf("historical request tool_calls = %#v, want empty []store.ToolCallEvent", empty["tool_calls"])
	}

	expected := []store.ToolCallEvent{{
		Seq: 1, Round: 1, ToolName: "web.search", Status: "succeeded",
		DurationMS: 35, ResultBytes: 128, CreatedAt: time.Date(2026, 11, 17, 7, 1, 0, 0, time.UTC),
	}}
	detail := buildRequestDetail(log, nil, false, nil, "", expected)
	actual, ok := detail["tool_calls"].([]store.ToolCallEvent)
	if !ok || len(actual) != 1 || actual[0] != expected[0] {
		t.Fatalf("tool_calls = %#v, want %#v", detail["tool_calls"], expected)
	}
	if detail["tool_call_count"] != 1 {
		t.Fatalf("tool_call_count = %#v, want 1", detail["tool_call_count"])
	}
}
