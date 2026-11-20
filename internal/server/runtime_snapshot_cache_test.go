package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

func TestSampleLlamaMetricsUsesResponseObservationTime(t *testing.T) {
	requestStarted := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Process-Start-Time-Unix", "12345")
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP llamacpp:requests_processing Number of requests processing\n# TYPE llamacpp:requests_processing gauge\nllamacpp:requests_processing 0\n"))
	}))
	defer srv.Close()

	s := &Server{}
	client := llama.New(srv.URL, time.Second, "llamacpp", "")
	s.sampleLlamaMetrics(context.Background(), client, "model-a")

	s.runtimeSnapshotSampleMu.RLock()
	sample := s.runtimeSnapshotSample
	s.runtimeSnapshotSampleMu.RUnlock()
	if sample == nil || sample.Metrics == nil {
		t.Fatalf("expected metrics sample, got %#v", sample)
	}
	observedAt := sample.Metrics.SampledAt
	if !observedAt.After(requestStarted.Add(30 * time.Millisecond)) {
		t.Fatalf("sampled_at must reflect response observation, not request start: started=%v observed=%v", requestStarted, observedAt)
	}
	if observedAt.After(time.Now()) {
		t.Fatalf("sampled_at cannot be in the future: %v", observedAt)
	}
}

func TestUpstreamRuntimeSnapshotCacheInvalidatesWhenUpstreamStops(t *testing.T) {
	var tagsHits atomic.Int32
	var upstreamStopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			tagsHits.Add(1)
			if upstreamStopped.Load() {
				http.Error(w, "upstream stopped", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"m:latest","model":"m:latest","size":1}]}`))
		case "/api/ps":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := &Server{}
	client := llama.New(srv.URL, 2*time.Second, "ollama", "")
	ctx := context.Background()

	snap1, err := s.upstreamRuntimeSnapshot(ctx, "ollama", client, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap1 == nil || len(snap1.Catalog) != 1 {
		t.Fatalf("expected 1 catalog row, got %#v err=%v", snap1, err)
	}
	if snap1.SampledAt.IsZero() {
		t.Fatal("fresh upstream snapshot must retain its actual sample time")
	}
	firstSampledAt := snap1.SampledAt
	time.Sleep(5 * time.Millisecond)
	cached, err := s.upstreamRuntimeSnapshot(ctx, "ollama", client, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cached.SampledAt.Equal(firstSampledAt) {
		t.Fatalf("cache hit must preserve original upstream sample time: first=%v cached=%v", firstSampledAt, cached.SampledAt)
	}
	if got := tagsHits.Load(); got != 1 {
		t.Fatalf("expected cache hit without another upstream fetch, got %d tag requests", got)
	}

	// 保留测试服务以便统计刷新请求；通过返回 503 模拟上游已不可用。
	upstreamStopped.Store(true)
	s.invalidateUpstreamRuntimeSnapshotCache()

	snap2, err2 := s.upstreamRuntimeSnapshot(ctx, "ollama", client, "")
	if err2 == nil && snap2 != nil && len(snap2.Catalog) == 1 {
		t.Fatal("expected cache miss / error after upstream stopped, still got catalog")
	}
	if tagsHits.Load() < 2 {
		t.Fatalf("expected at least 2 tag fetches (initial + after stop), got %d", tagsHits.Load())
	}
}
