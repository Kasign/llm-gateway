package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestLocalRuntimeBackendRejectsRemoteProvider(t *testing.T) {
	for _, id := range []string{"deepseek", "openai"} {
		if got, err := localRuntimeBackend(id); err == nil || got != "" {
			t.Errorf("localRuntimeBackend(%q) = %q, %v; want rejection", id, got, err)
		}
	}
	for _, id := range []string{"llamacpp", "ollama"} {
		if got, err := localRuntimeBackend(id); err != nil || got != id {
			t.Errorf("localRuntimeBackend(%q) = %q, %v; want local backend", id, got, err)
		}
	}
}

func TestBuildAdminRuntimeOverviewRemoteDoesNotExposeLocalModelState(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
	}}
	s := &Server{cfg: cfg, upstream: settings.Upstream{
		Backend: "deepseek", BaseURL: "https://api.deepseek.com/v1", ModelName: "deepseek-chat",
	}}
	got, err := s.buildAdminRuntimeOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["remote_provider"] != true || got["local_model_management"] != false {
		t.Fatalf("remote runtime capabilities = %#v", got)
	}
	if got["backend"] != "deepseek" || got["active_model"] != "deepseek-chat" {
		t.Fatalf("remote runtime identity = %#v", got)
	}
	if _, ok := got["loaded"].([]interface{}); !ok {
		t.Fatalf("remote runtime should not expose stale local loaded model records: %#v", got)
	}
}
