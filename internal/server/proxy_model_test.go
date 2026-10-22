package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestSelectSwitchModelNameDoesNotCarryOtherBackend(t *testing.T) {
	cur := settings.Upstream{Backend: "llamacpp", ModelName: "qwen.gguf"}
	name, byBackend := selectSwitchModelName(cur, "ollama", "", "")
	if name != "" {
		t.Fatalf("ollama model = %q, want empty until that backend has its own model", name)
	}
	if byBackend["llamacpp"] != "qwen.gguf" {
		t.Fatalf("llamacpp memory = %#v", byBackend)
	}
	if _, ok := byBackend["ollama"]; ok {
		t.Fatalf("ollama should not inherit llama.cpp name: %#v", byBackend)
	}
}

func TestSelectSwitchModelNameRestoresBackendMemory(t *testing.T) {
	cur := settings.Upstream{
		Backend:   "ollama",
		ModelName: "qwen3.5:9b",
		ModelsByBackend: map[string]string{
			"llamacpp": "qwen.gguf",
		},
	}
	name, byBackend := selectSwitchModelName(cur, "llamacpp", "", "")
	if name != "qwen.gguf" {
		t.Fatalf("restored model = %q", name)
	}
	if byBackend["ollama"] != "qwen3.5:9b" || byBackend["llamacpp"] != "qwen.gguf" {
		t.Fatalf("memory = %#v", byBackend)
	}
}

func TestSelectSwitchModelNameExplicitWins(t *testing.T) {
	cur := settings.Upstream{Backend: "llamacpp", ModelName: "old.gguf"}
	name, _ := selectSwitchModelName(cur, "ollama", "qwen3.5:9b", "")
	if name != "qwen3.5:9b" {
		t.Fatalf("name = %q", name)
	}
	name, _ = selectSwitchModelName(cur, "llamacpp", "", "")
	if name != "old.gguf" {
		t.Fatalf("same backend name = %q", name)
	}
}

func TestProxyModelIDFromSnapshot(t *testing.T) {
	loaded := &llama.RuntimeSnapshot{
		Backend: "ollama",
		Loaded:  []llama.CatalogItem{{ID: "in-memory:latest", Loaded: true}},
		Catalog: []llama.CatalogItem{{ID: "other:latest"}, {ID: "in-memory:latest"}},
	}
	if got := proxyModelIDFromSnapshot(loaded, "ollama"); got != "in-memory:latest" {
		t.Fatalf("loaded model = %q", got)
	}

	only := &llama.RuntimeSnapshot{
		Backend: "ollama",
		Catalog: []llama.CatalogItem{{ID: "qwen3.5:9b"}},
	}
	if got := proxyModelIDFromSnapshot(only, "ollama"); got != "qwen3.5:9b" {
		t.Fatalf("single catalog model = %q", got)
	}

	many := &llama.RuntimeSnapshot{
		Backend: "ollama",
		Catalog: []llama.CatalogItem{{ID: "a:latest"}, {ID: "b:latest"}},
	}
	if got := proxyModelIDFromSnapshot(many, "ollama"); got != "" {
		t.Fatalf("ambiguous catalog model = %q", got)
	}
}

func TestSelectSwitchModelNamePrefersExplicitLoadID(t *testing.T) {
	cur := settings.Upstream{Backend: "llamacpp", ModelName: "local.gguf"}
	got, _ := selectSwitchModelName(cur, "deepseek", "", "deepseek-reasoner")
	if got != "deepseek-reasoner" {
		t.Fatalf("model name = %q, want explicit model ID", got)
	}
}

func TestResolveProxyModelTargetRemoteUsesProviderModelVerbatim(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1",
		DefaultModel: "deepseek-chat",
	}}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "deepseek", ModelName: "deepseek-chat"},
	}
	got, err := s.resolveProxyModelTarget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != nil || got.ModelKey != "deepseek-chat" || got.UpstreamID != "deepseek-chat" || got.Hint != "deepseek-chat" {
		t.Fatalf("remote model target = %+v", got)
	}
}

func TestResolveProxyModelTargetRemoteUsesConfiguredDefaultWhenRuntimeNameEmpty(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "openai", Enabled: true, BaseURL: "https://api.openai.com/v1",
		DefaultModel: "gpt-4.1",
	}}
	s := &Server{cfg: cfg, upstream: settings.Upstream{Backend: "openai"}}
	got, err := s.resolveProxyModelTarget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != nil || got.ModelKey != "gpt-4.1" || got.UpstreamID != "gpt-4.1" {
		t.Fatalf("remote default model target = %+v", got)
	}
}
