package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
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



type fakeForwardProvider struct {
	id     string
	result core.Result
	err    error
}

func (p fakeForwardProvider) ID() string { return p.id }

func (p fakeForwardProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Protocol: "test"}
}

func (p fakeForwardProvider) Forward(context.Context, provider.InferenceCall) (core.Result, error) {
	return p.result, p.err
}

func TestInferenceForwarderPreservesProviderTimeoutStatus(t *testing.T) {
	registry := provider.NewRegistry()
	if err := registry.Register("remote-test", func() provider.Provider {
		return fakeForwardProvider{
			id:  "remote-test",
			err: provider.NewHTTPError("remote-test", http.StatusGatewayTimeout, errors.New("timeout")),
		}
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{providers: registry, upstream: settings.Upstream{Backend: "remote-test"}}
	st := core.State{
		Request: core.Request{
			ID: "forward-timeout-test", Method: http.MethodPost, Path: "/v1/chat/completions",
			Started: time.Now(), Sink: &responseSink{w: httptest.NewRecorder()},
			Header: make(http.Header),
		},
		Model: core.ModelChoice{UpstreamID: "remote-model"},
	}
	result, err := (inferenceForwarder{s: s}).Forward(context.Background(), st, nil)
	if err == nil {
		t.Fatal("expected provider timeout error")
	}
	if result.Status != http.StatusGatewayTimeout {
		t.Fatalf("result status = %d, want 504", result.Status)
	}
	var coreErr *core.Error
	if !errors.As(err, &coreErr) || coreErr.Status != http.StatusGatewayTimeout {
		t.Fatalf("error = %#v, want core 504 error", err)
	}
}

func TestInferenceForwarderUsesBadGatewayForUnclassifiedTransportError(t *testing.T) {
	registry := provider.NewRegistry()
	if err := registry.Register("remote-test", func() provider.Provider {
		return fakeForwardProvider{id: "remote-test", err: errors.New("connection reset")}
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{providers: registry, upstream: settings.Upstream{Backend: "remote-test"}}
	st := core.State{
		Request: core.Request{
			ID: "forward-transport-error-test", Method: http.MethodPost, Path: "/v1/chat/completions",
			Started: time.Now(), Sink: &responseSink{w: httptest.NewRecorder()},
			Header: make(http.Header),
		},
		Model: core.ModelChoice{UpstreamID: "remote-model"},
	}
	result, err := (inferenceForwarder{s: s}).Forward(context.Background(), st, nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
	if result.Status != http.StatusBadGateway {
		t.Fatalf("result status = %d, want 502", result.Status)
	}
}
