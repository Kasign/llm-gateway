package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/settings"
)

type pinTestProvider struct {
	id      string
	calls   int
	modelID string
}

func (p *pinTestProvider) ID() string { return p.id }

func (p *pinTestProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Protocol: "test", ChatCompletions: true, Streaming: true}
}

func (p *pinTestProvider) Forward(_ context.Context, call provider.InferenceCall) (core.Result, error) {
	p.calls++
	p.modelID = call.ModelID
	return core.Result{Status: http.StatusOK}, nil
}

func TestInferenceForwarderUsesProviderPinnedBeforeRuntimeSwitch(t *testing.T) {
	oldProvider := &pinTestProvider{id: "remote-old"}
	newProvider := &pinTestProvider{id: "remote-new"}
	registry := provider.NewRegistry()
	if err := registry.Register(oldProvider.id, func() provider.Provider { return oldProvider }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(newProvider.id, func() provider.Provider { return newProvider }); err != nil {
		t.Fatal(err)
	}

	// 模拟 Resolve 完成后管理员已经切换到 remote-new。
	s := &Server{providers: registry, upstream: settings.Upstream{Backend: "remote-new"}}
	recorder := httptest.NewRecorder()
	st := core.State{
		Request: core.Request{
			ID: "provider-pin-forward", Method: http.MethodPost, Path: "/v1/chat/completions",
			Header: make(http.Header), Sink: &responseSink{w: recorder},
		},
		Model: core.ModelChoice{ProviderID: "remote-old", UpstreamID: "model-from-old"},
	}
	result, err := (inferenceForwarder{s: s}).Forward(context.Background(), st, nil)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if oldProvider.calls != 1 || oldProvider.modelID != "model-from-old" {
		t.Fatalf("pinned provider calls/model = %d/%q, want 1/model-from-old", oldProvider.calls, oldProvider.modelID)
	}
	if newProvider.calls != 0 {
		t.Fatalf("new active provider was unexpectedly called %d times", newProvider.calls)
	}
}

func TestResolveProxyModelTargetUsesSuppliedUpstreamSnapshot(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "openai", ModelName: "gpt-4.1"},
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
			"openai": {ID: "openai", Enabled: true, BaseURL: "https://api.openai.com/v1", APIKeyHeader: "authorization", DefaultModel: "gpt-4.1"},
		},
	}
	target, err := s.resolveProxyModelTargetForUpstream(context.Background(), settings.Upstream{
		Backend: "deepseek", BaseURL: "https://api.deepseek.com/v1", ModelName: "deepseek-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.ProviderID != "deepseek" || target.ModelKey != "deepseek-chat" || target.UpstreamID != "deepseek-chat" {
		t.Fatalf("target = %+v, want pinned deepseek-chat target", target)
	}
}

func TestProviderSensitiveMutatorsUsePinnedProviderID(t *testing.T) {
	localBody := []byte("{\"stream\":true,\"input\":[{\"role\":\"developer\",\"content\":\"system rule\"},{\"role\":\"user\",\"content\":\"hello\"}]}")
	localState := core.State{
		Request: core.Request{Path: "/v1/responses", Body: append([]byte(nil), localBody...)},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "local-key", UpstreamID: "local-model"},
	}
	s := &Server{upstream: settings.Upstream{Backend: "deepseek"}}
	got, err := s.mutateSanitize(context.Background(), localState)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(got.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["instructions"] != "system rule" {
		t.Fatalf("pinned local sanitizer instructions = %#v", payload["instructions"])
	}
	input, _ := payload["input"].([]interface{})
	if len(input) != 1 {
		t.Fatalf("pinned local sanitizer input length = %d, want 1", len(input))
	}

	// 当前活动后端是本地，但固定到远程 Provider 的请求体必须保持原样。
	remoteState := core.State{
		Request: core.Request{Path: "/v1/responses", Body: append([]byte(nil), localBody...)},
		Model: core.ModelChoice{ProviderID: "deepseek", ModelKey: "remote-model", UpstreamID: "deepseek-chat"},
	}
	s.upstream = settings.Upstream{Backend: "llamacpp"}
	got, err = s.mutateSanitize(context.Background(), remoteState)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != string(localBody) {
		t.Fatalf("pinned remote body changed: got %s want %s", got.Body, localBody)
	}
	got, err = s.mutateInferenceParams(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != string(localBody) {
		t.Fatalf("pinned remote params changed: got %s want %s", got.Body, localBody)
	}
}

type pinTestLocalManager struct {
	calls int
	key   string
	model string
}

func (m *pinTestLocalManager) EnsureReady(_ context.Context, modelKey, upstreamModelID string) error {
	m.calls++
	m.key, m.model = modelKey, upstreamModelID
	return nil
}
func (m *pinTestLocalManager) Load(context.Context, string) error   { return nil }
func (m *pinTestLocalManager) Unload(context.Context, string) error { return nil }
func (m *pinTestLocalManager) Refresh(context.Context) error        { return nil }

func TestGuardModelReadyUsesPinnedProviderManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.Llama.AutoLoadOnProxy = true
	llamaManager, ollamaManager := &pinTestLocalManager{}, &pinTestLocalManager{}
	registry := provider.NewRegistry()
	if err := registry.RegisterLocal("llamacpp", func() provider.Provider { return &pinTestProvider{id: "llamacpp"} }, llamaManager); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterLocal("ollama", func() provider.Provider { return &pinTestProvider{id: "ollama"} }, ollamaManager); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, providers: registry, upstream: settings.Upstream{Backend: "deepseek"}}
	const requestID = "provider-pin-manager"
	s.callBegin(requestID, nil)
	defer s.callDrop(requestID)
	st := core.State{
		Request: core.Request{ID: requestID, Path: "/v1/chat/completions"},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "gateway-key", UpstreamID: "upstream-model"},
	}
	if err := s.guardModelReady(context.Background(), st); err != nil {
		t.Fatalf("guardModelReady: %v", err)
	}
	if llamaManager.calls != 1 || llamaManager.key != "gateway-key" || llamaManager.model != "upstream-model" {
		t.Fatalf("pinned manager call = %+v", llamaManager)
	}
	if ollamaManager.calls != 0 {
		t.Fatalf("non-selected manager called %d times", ollamaManager.calls)
	}
}

func TestLocalClientForBackendCachesIndependently(t *testing.T) {
	cfg := &config.Config{}
	cfg.Llama.LlamaCppBaseURL = "http://127.0.0.1:18080"
	cfg.Llama.OllamaBaseURL = "http://127.0.0.1:11434"
	s := &Server{cfg: cfg}
	llamaFirst, err := s.localClientForBackend("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	llamaSecond, err := s.localClientForBackend("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	ollama, err := s.localClientForBackend("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if llamaFirst != llamaSecond {
		t.Fatal("same backend should reuse its cached HTTP client")
	}
	if llamaFirst == ollama {
		t.Fatal("different backends must not share HTTP clients")
	}
	cfg.Llama.OllamaBaseURL = "http://127.0.0.1:11435"
	ollamaUpdated, err := s.localClientForBackend("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if ollamaUpdated == ollama {
		t.Fatal("BaseURL change must replace that backend's cached client")
	}
}
