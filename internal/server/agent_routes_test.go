package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
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
