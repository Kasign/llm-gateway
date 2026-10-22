package settings

import (
 "strings"
 "testing"
)

func TestUpstreamValidatesRegisteredStyleBackendIDs(t *testing.T) {
	for _, backend := range []string{"llamacpp", "ollama", "openai", "deepseek.compat"} {
		u := Upstream{Backend: backend, BaseURL: "https://provider.example/v1"}
		if err := u.Validate(); err != nil {
			t.Errorf("backend %q should be structurally valid: %v", backend, err)
		}
	}
	for _, backend := range []string{"", "../admin", "bad id", "-starts-with-dash", strings.Repeat("a", 65)} {
		if err := (Upstream{Backend: backend, BaseURL: "https://provider.example/v1"}).Validate(); err == nil {
			t.Errorf("backend %q should be rejected", backend)
		}
	}
}

func TestNormalizeBackendModelsKeepsRemoteProviderIDs(t *testing.T) {
	got := normalizeBackendModels(map[string]string{
		"deepseek": "deepseek-chat",
		"ollama":   "qwen3:8b",
		"BAD ID":   "ignored",
	})
	if got["deepseek"] != "deepseek-chat" || got["ollama"] != "qwen3:8b" {
		t.Fatalf("normalized map = %#v", got)
	}
	if _, ok := got["bad id"]; ok {
		t.Fatalf("invalid ID should be dropped: %#v", got)
	}
}
