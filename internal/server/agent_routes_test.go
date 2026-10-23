package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
)

func TestAgentRouteHandlerStripsPrefixAndPreservesPublicURL(t *testing.T) {
	cfg := config.AgentRouteConfig{
		ID: "deepseek", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}
	s := &Server{}
	var gotPath, gotProvider, gotModel, publicURL string
	handler := s.agentRouteHandler(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		route, ok := agentRouteFromContext(r.Context())
		if !ok {
			t.Fatal("trusted Agent route missing from context")
		}
		gotProvider, gotModel = route.ProviderID, route.DefaultModel
		publicURL = agentRequestPublicURL(r, nil)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://gateway.example/deepseek/v1/chat/completions?token=private&stream=true", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d", res.Code)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("normalized path = %q", gotPath)
	}
	if gotProvider != "deepseek" || gotModel != "deepseek-chat" {
		t.Fatalf("route = provider %q model %q", gotProvider, gotModel)
	}
	if !strings.Contains(publicURL, "/deepseek/v1/chat/completions?token=***&stream=true") {
		t.Fatalf("public URL was not preserved/redacted: %q", publicURL)
	}
}

func TestResolveInferenceUsesAgentRouteWithoutChangingActiveProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
	}}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://llama:8080", ModelName: "local.gguf"},
	}
	choice, err := s.resolveInference(context.Background(), core.State{
		Request: core.Request{
			ID: "agent-route-test", Method: http.MethodPost, Path: "/v1/chat/completions",
			RouteProviderID: "deepseek", RouteDefaultModel: "deepseek-chat",
		},
		ClientModel: "caller-model-must-not-select-provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if choice.ProviderID != "deepseek" || choice.UpstreamID != "deepseek-chat" || choice.ModelKey != "deepseek-chat" {
		t.Fatalf("route model choice = %+v", choice)
	}
	if s.effectiveUpstream().Backend != "llamacpp" {
		t.Fatalf("Agent routing mutated active Provider: %q", s.effectiveUpstream().Backend)
	}
}

func TestRejectedInferenceLogPreservesAgentURLAndRedactsQuerySecrets(t *testing.T) {
	cfg := config.AgentRouteConfig{
		ID: "deepseek", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}
	s := &Server{}
	var logItem store.RequestLog
	handler := s.agentRouteHandler(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		logItem = s.buildRejectedInferenceLog(r, "rejected-agent-request", now, now, nil,
			http.StatusUnauthorized, "invalid_api_key", "Invalid API key")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	req := httptest.NewRequest(http.MethodPost,
		"http://gateway.example/deepseek/v1/chat/completions?token=private&stream=true", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
	if !strings.Contains(logItem.RequestURL, "/deepseek/v1/chat/completions?token=***&stream=true") {
		t.Fatalf("rejected Agent request URL lost its public prefix or redaction: %q", logItem.RequestURL)
	}
	if strings.Contains(logItem.RequestURL, "token=private") {
		t.Fatalf("rejected Agent request audit URL leaked query credential: %q", logItem.RequestURL)
	}
}


func TestConcurrentAgentRoutesKeepProviderAndModelIsolation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{
		{ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat"},
		{ID: "openai", Enabled: true, BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4.1"},
	}
	s := &Server{
		cfg: cfg,
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://llama:8080", ModelName: "local.gguf"},
	}

	const iterations = 200
	var wg sync.WaitGroup
	errors := make(chan string, iterations*2)
	for i := 0; i < iterations; i++ {
		for _, tc := range []struct {
			provider string
			model    string
		}{{"deepseek", "deepseek-chat"}, {"openai", "gpt-4.1"}} {
			wg.Add(1)
			go func(i int, providerID, model string) {
				defer wg.Done()
				choice, err := s.resolveInference(context.Background(), core.State{
					Request: core.Request{
						ID: fmt.Sprintf("agent-concurrent-%s-%d", providerID, i),
						Method: http.MethodPost, Path: "/v1/chat/completions",
						RouteProviderID: providerID, RouteDefaultModel: model,
					},
					// Client-provided model is intentionally different; it must never switch a pinned Agent Provider.
					ClientModel: "caller-model-must-not-select-provider",
				})
				if err != nil {
					errors <- fmt.Sprintf("provider %s: resolve failed: %v", providerID, err)
					return
				}
				if choice.ProviderID != providerID || choice.UpstreamID != model || choice.ModelKey != model {
					errors <- fmt.Sprintf("provider %s model %s resolved to %+v", providerID, model, choice)
				}
			}(i, tc.provider, tc.model)
		}
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if got := s.effectiveUpstream().Backend; got != "llamacpp" {
		t.Fatalf("concurrent Agent resolution mutated the global active Provider: %q", got)
	}
}
