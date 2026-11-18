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
			t.Errorf("unexpected path %q", r.URL.Path)
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


func TestRuntimeSnapshotSampleLatestReturnsCopy(t *testing.T) {
	s := &Server{}
	snap := &llama.RuntimeSnapshot{
		Backend:    "ollama",
		ActiveModel: "m:latest",
		Loaded:     []llama.CatalogItem{{ID: "m:latest", Loaded: true, RAMBytes: 10}},
		Catalog:    []llama.CatalogItem{{ID: "m:latest", DiskBytes: 20}},
	}
	s.setRuntimeSnapshotSample(snap, "complete", "upstream_runtime")

	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.Snapshot == nil {
		t.Fatal("expected runtime snapshot")
	}
	if got.Status != "complete" || got.Source != "upstream_runtime" || got.Snapshot.Backend != "ollama" {
		t.Fatalf("unexpected sample: %#v", got)
	}
	if got.SampledAt.IsZero() || time.Since(got.SampledAt) < 0 {
		t.Fatalf("invalid sample timestamp: %v", got.SampledAt)
	}

	got.Snapshot.Loaded[0].RAMBytes = 999
	got.Snapshot.Catalog[0].DiskBytes = 999
	again := s.runtimeSnapshotSampleLatest()
	if again.Snapshot.Loaded[0].RAMBytes != 10 || again.Snapshot.Catalog[0].DiskBytes != 20 {
		t.Fatal("latest snapshot must be isolated from caller mutation")
	}
}

func TestRuntimeSnapshotSampleUnavailable(t *testing.T) {
	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "unavailable", "upstream_runtime")
	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.Snapshot != nil || got.Status != "unavailable" {
		t.Fatalf("unexpected unavailable sample: %#v", got)
	}
}
