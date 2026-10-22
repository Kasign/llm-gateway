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

func TestFlattenHeadersRedactsProviderCredentialHeaders(t *testing.T) {
	h := map[string][]string{
		"Proxy-Authorization": {"Basic dXNlcjpwYXNz"},
		"OpenAI-API-Key":      {"sk-provider-secret"},
		"X-Access-Token":      {"private-token"},
		"Client-Secret":       {"client-secret"},
		"X-Codex-Session":     {"session-123"},
	}
	got := flattenHeaders(h)
	for _, key := range []string{"Proxy-Authorization", "OpenAI-API-Key", "X-Access-Token", "Client-Secret"} {
		if got[key] != "***" {
			t.Errorf("%s = %q, want redacted value", key, got[key])
		}
	}
	if got["X-Codex-Session"] != "session-123" {
		t.Fatalf("non-secret unknown header should remain visible: %#v", got)
	}
}
