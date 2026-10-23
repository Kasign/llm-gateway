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
		"Proxy-Authorization":      {"Basic dXNlcjpwYXNz"},
		"OpenAI-API-Key":           {"sk-provider-secret"},
		"X-Access-Token":           {"private-token"},
		"Client-Secret":            {"client-secret"},
		"X-OpenAI-Key":             {"provider-key-secret"},
		"X-DeepSeek-Key":           {"deepseek-key-secret"},
		"X-Gemini-Key":             {"gemini-key-secret"},
		"X-Provider-Secret-Key":    {"provider-secret-key"},
		"X-Amz-Credential":         {"AKIA-secret"},
		"X-Codex-Session":          {"session-123"},
		"X-Request-Idempotency-Key": {"non-secret-request-key"},
		"X-Model-Key":              {"model-key"},
	}
	got := flattenHeaders(h)
	for _, key := range []string{"Proxy-Authorization", "OpenAI-API-Key", "X-Access-Token", "Client-Secret", "X-OpenAI-Key", "X-DeepSeek-Key", "X-Gemini-Key", "X-Provider-Secret-Key", "X-Amz-Credential"} {
		if got[key] != "***" {
			t.Errorf("%s = %q, want redacted value", key, got[key])
		}
	}
	if got["X-Codex-Session"] != "session-123" || got["X-Request-Idempotency-Key"] != "non-secret-request-key" || got["X-Model-Key"] != "model-key" {
		t.Fatalf("non-secret headers should remain visible: %#v", got)
	}
}
