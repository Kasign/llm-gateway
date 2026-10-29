package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/llama"
)

func TestLlamaLoadNotFoundRequiresRuntimeConfirmation(t *testing.T) {
	tests := []struct {
		name             string
		routerModels     string
		openAIModels     string
		wantLoadedID     string
		wantErr          bool
		wantRouterCached bool
	}{
		{
			name:             "single-model endpoint already serves target",
			routerModels:     `{"object":"list","data":[{"id":"target.gguf"}]}`,
			openAIModels:     `{"object":"list","data":[{"id":"target.gguf"}]}`,
			wantLoadedID:     "target.gguf",
			wantRouterCached: false,
		},
		{
			name:             "single-model endpoint serves a different model",
			routerModels:     `{"object":"list","data":[{"id":"other.gguf"}]}`,
			openAIModels:     `{"object":"list","data":[{"id":"other.gguf"}]}`,
			wantErr:          true,
			wantRouterCached: false,
		},
		{
			name:             "router catalog has target but it is unloaded",
			routerModels:     `{"object":"list","data":[{"id":"target.gguf","status":{"value":"unloaded"}}]}`,
			openAIModels:     `{"object":"list","data":[{"id":"target.gguf"}]}`,
			wantErr:          true,
			wantRouterCached: true,
		},
		{
			name:             "router runtime confirms target already loaded",
			routerModels:     `{"object":"list","data":[{"id":"target.gguf","status":{"value":"loaded"}}]}`,
			openAIModels:     `{"object":"list","data":[{"id":"target.gguf"}]}`,
			wantLoadedID:     "target.gguf",
			wantRouterCached: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/models/load" && r.Method == http.MethodPost:
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"load endpoint not found"}}`))
				case r.URL.Path == "/models":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.routerModels))
				case r.URL.Path == "/v1/models":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tc.openAIModels))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()

			routerEnabled := true
			s := &Server{
				cfg:         &config.Config{},
				llamaRouter: &routerEnabled,
			}
			client := llama.New(upstream.URL, 2*time.Second, "llamacpp", "")

			got, err := s.loadLlamaCppUpstreamWithClient(
				context.Background(),
				client,
				nil,
				"target.gguf",
				upstream.URL,
				true,
				nil,
			)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("load returned success for an unconfirmed target: got %q", got)
				}
			} else {
				if err != nil {
					t.Fatalf("load returned error: %v", err)
				}
				if got != tc.wantLoadedID {
					t.Fatalf("loaded ID = %q, want %q", got, tc.wantLoadedID)
				}
			}
			if s.llamaRouter == nil || *s.llamaRouter != tc.wantRouterCached {
				t.Fatalf("router cache = %v, want %v", s.llamaRouter, tc.wantRouterCached)
			}
		})
	}
}
