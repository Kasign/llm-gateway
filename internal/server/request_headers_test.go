package server

import "testing"

func TestFlattenHeadersKeepsUnknownFields(t *testing.T) {
	h := map[string][]string{
		"Accept":          {"text/event-stream"},
		"Authorization":   {"Bearer sk-secret"},
		"X-API-Key":       {"abc"},
		"X-Codex-Session": {"sess-1"},
		"User-Agent":      {"codex/1"},
	}
	got := flattenHeaders(h)
	if got["Authorization"] != "Bearer ***" || got["X-API-Key"] != "***" {
		t.Fatalf("secrets not redacted: %#v", got)
	}
	if got["Accept"] != "text/event-stream" || got["User-Agent"] != "codex/1" || got["X-Codex-Session"] != "sess-1" {
		t.Fatalf("full header map not kept: %#v", got)
	}
}
