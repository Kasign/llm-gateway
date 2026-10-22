package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestRemoteProviderRuntimeResolutionUsesConfiguredURLAndDefaultModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Llama.LlamaCppBaseURL = "http://llama:8080"
	cfg.Llama.OllamaBaseURL = "http://ollama:11434"
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1",
		APIKey: "sk-never-return-this", DefaultModel: "deepseek-chat",
	}}
	s := &Server{cfg: cfg, fileUpstream: settings.Upstream{ModelsDir: "/models"}}
	got := s.resolveUpstream(settings.Upstream{Backend: "DEEPSEEK"})
	if got.Backend != "deepseek" || got.BaseURL != "https://api.deepseek.com/v1" || got.ModelName != "deepseek-chat" || got.ModelsDir != "" {
		t.Fatalf("resolved remote upstream = %+v", got)
	}
	if !s.isConfiguredProvider("deepseek") || !s.isConfiguredProvider("llamacpp") || s.isConfiguredProvider("unknown") {
		t.Fatal("configured-provider lookup returned incorrect result")
	}
	backends := s.availableBackends()
	found := false
	for _, backend := range backends {
		if backend["id"] == "deepseek" {
			found = true
			if backend["api_key_configured"] != true {
				t.Fatal("available backend should expose whether a key is configured, not the key itself")
			}
		}
	}
	if !found {
		t.Fatalf("remote provider absent from available backends: %#v", backends)
	}
}

