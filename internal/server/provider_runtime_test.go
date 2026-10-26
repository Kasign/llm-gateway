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
	"github.com/whl/llm-gateway/internal/store"
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


func TestProviderIDForStateFollowsResolverTarget(t *testing.T) {
	s := &Server{upstream: settings.Upstream{Backend: "llamacpp"}}
	cases := []struct {
		name string
		state core.State
		want string
	}{
		{name: "resolved model wins", state: core.State{
			Request: core.Request{RouteProviderID: "deepseek"},
			Model:   core.ModelChoice{ProviderID: "openai"},
		}, want: "openai"},
		{name: "trusted agent route before active upstream", state: core.State{
			Request: core.Request{RouteProviderID: "deepseek"},
		}, want: "deepseek"},
		{name: "service provider wins for internal service model", state: core.State{
			Request: core.Request{ServiceModel: "internal-model", ServiceProviderID: "openai", RouteProviderID: "deepseek"},
		}, want: "openai"},
		{name: "service model without provider falls back to active", state: core.State{
			Request: core.Request{ServiceModel: "internal-model", RouteProviderID: "deepseek"},
		}, want: "llamacpp"},
		{name: "active provider fallback", state: core.State{}, want: "llamacpp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.providerIDForState(tc.state); got != tc.want {
				t.Fatalf("providerIDForState() = %q, want %q", got, tc.want)
			}
		})
	}
}


func TestMergeLiveIntoLogPreservesResolvedProviderID(t *testing.T) {
	base := store.RequestLog{RequestID: "req-1", ProviderID: "llamacpp"}
	live := store.RequestLog{RequestID: "req-1", ProviderID: "deepseek"}
	got := mergeLiveIntoLog(base, live)
	if got.ProviderID != "deepseek" {
		t.Fatalf("merged provider_id = %q, want deepseek", got.ProviderID)
	}
}

func TestRequestLogMatchesProviderUsesNormalizedExactID(t *testing.T) {
	row := store.RequestLog{ProviderID: "DeepSeek"}
	cases := []struct {
		name      string
		provider  string
		wantMatch bool
	}{
		{name: "case insensitive", provider: "deepseek", wantMatch: true},
		{name: "trim spaces", provider: "  DEEPSEEK  ", wantMatch: true},
		{name: "exact ID only", provider: "deep", wantMatch: false},
		{name: "empty filter matches all", provider: "", wantMatch: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestLogMatchesProvider(row, tc.provider); got != tc.wantMatch {
				t.Fatalf("requestLogMatchesProvider(%q) = %v, want %v", tc.provider, got, tc.wantMatch)
			}
		})
	}
}
