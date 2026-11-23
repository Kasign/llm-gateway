package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
)

func TestBenchmarkModelLoadStateBeforeUsesFreshPreflightSnapshot(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch r.URL.Path {
		case "/models":
			// 非 router 模式：RuntimeSnapshot 应回退到 OpenAI 兼容模型列表。
			http.NotFound(w, r)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "model-a"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Llama.LlamaCppBaseURL = upstream.URL
	s := &Server{cfg: cfg}

	state, source := s.benchmarkModelLoadStateBefore(context.Background(), "llamacpp", "model-a")
	if state != "loaded" || source != "benchmark_preflight" {
		t.Fatalf("preflight result = (%q, %q), want (loaded, benchmark_preflight)", state, source)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("preflight made %d requests, want only GET /models and GET /v1/models", got)
	}
	cached := s.runtimeSnapshotSampleLatest()
	if cached == nil || cached.Source != "benchmark_preflight" || cached.Status != "complete" {
		t.Fatalf("preflight snapshot was not retained for runtime diagnostics: %#v", cached)
	}
}

func TestBenchmarkModelLoadStateBeforeKeepsUnknownWhenPreflightFails(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Llama.LlamaCppBaseURL = upstream.URL
	s := &Server{cfg: cfg}

	state, source := s.benchmarkModelLoadStateBefore(context.Background(), "llamacpp", "model-a")
	if state != "unknown" || source != "benchmark_preflight_failed" {
		t.Fatalf("failed preflight result = (%q, %q), want (unknown, benchmark_preflight_failed)", state, source)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("failed preflight made %d requests, want exactly two read-only model-list requests", got)
	}
	if cached := s.runtimeSnapshotSampleLatest(); cached != nil && cached.Source == "benchmark_preflight" {
		t.Fatalf("failed preflight must not publish a successful snapshot: %#v", cached)
	}
}
