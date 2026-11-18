package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whl/llm-gateway/internal/llama"
)

func TestSampleLlamaMetricsStoresDynamicSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# TYPE runtime_new_metric gauge\nruntime_new_metric 0.5\n"))
	}))
	defer srv.Close()

	s := &Server{}
	client := llama.New(srv.URL, 0, "llamacpp", "")
	s.sampleLlamaMetrics(context.Background(), client, "")
	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.MetricsStatus != "complete" || got.Metrics == nil || len(got.Metrics.Samples) != 1 {
		t.Fatalf("unexpected metrics sample: %#v", got)
	}
	if got.Metrics.Samples[0].Name != "runtime_new_metric" || got.Metrics.Samples[0].Type != llama.MetricTypeGauge {
		t.Fatalf("unexpected metric: %#v", got.Metrics.Samples[0])
	}
}

func TestSampleLlamaMetricsKeepsUnsupportedSeparate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer srv.Close()

	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	s.sampleLlamaMetrics(context.Background(), llama.New(srv.URL, 0, "llamacpp", ""), "")
	got := s.runtimeSnapshotSampleLatest()
	if got.Status != "complete" || got.MetricsStatus != "unsupported" || got.Metrics != nil {
		t.Fatalf("metrics failure changed runtime status: %#v", got)
	}
}

func TestRuntimeSnapshotSampleCopiesMetricsLabels(t *testing.T) {
	s := &Server{}
	s.setRuntimeMetricsSample(&llama.MetricsSnapshot{Samples: []llama.MetricSample{{Name: "metric", Labels: map[string]string{"model": "m"}}}}, "complete", "")
	got := s.runtimeSnapshotSampleLatest()
	got.Metrics.Samples[0].Labels["model"] = "mutated"
	again := s.runtimeSnapshotSampleLatest()
	if again.Metrics.Samples[0].Labels["model"] != "m" {
		t.Fatal("metrics snapshot must be isolated from caller mutation")
	}
}
