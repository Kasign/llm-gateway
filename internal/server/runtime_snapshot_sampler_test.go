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

func TestSampleLlamaMetricsStoresDynamicSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Process-Start-Time-Unix", "256533562943")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("# TYPE runtime_new_metric gauge\nruntime_new_metric 0.5\n"))
	}))
	defer srv.Close()

	s := &Server{}
	client := llama.New(srv.URL, 0, "llamacpp", "")
	s.sampleLlamaMetrics(context.Background(), client, "model-a")
	got := s.runtimeSnapshotSampleLatest()
	if got == nil || got.MetricsStatus != "complete" || got.Metrics == nil || len(got.Metrics.Samples) != 1 {
		t.Fatalf("unexpected metrics sample: %#v", got)
	}
	if got.Metrics.Samples[0].Name != "runtime_new_metric" || got.Metrics.Samples[0].Type != llama.MetricTypeGauge {
		t.Fatalf("unexpected metric: %#v", got.Metrics.Samples[0])
	}
	if got.Metrics.ProcessInstanceID == nil || *got.Metrics.ProcessInstanceID != "256533562943" || got.Metrics.ModelScope != "model-a" {
		t.Fatalf("process identity or model scope was not retained: %#v", got.Metrics)
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

func TestSetRuntimeSnapshotSamplePreservesPreviousMetricsForDelta(t *testing.T) {
	s := &Server{}
	processStart := "256533562943"
	s.setRuntimeMetricsSample(&llama.MetricsSnapshot{
		ProcessInstanceID: &processStart,
		ModelScope: "model-a",
		Samples: []llama.MetricSample{{Name: "requests_total", Type: llama.MetricTypeCounter, Value: "10"}},
	}, "complete", "")
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	got := s.runtimeSnapshotSampleLatest()
	if got.Metrics == nil || got.Metrics.ProcessInstanceID == nil || *got.Metrics.ProcessInstanceID != processStart {
		t.Fatalf("runtime snapshot update must preserve the previous metrics sample: %#v", got)
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


func TestRuntimeMetricsOverviewReturnsDynamicSamples(t *testing.T) {
	s := &Server{}
	s.setRuntimeMetricsSample(&llama.MetricsSnapshot{
		SampledAt: time.Now().UTC(),
		Samples: []llama.MetricSample{{Name: "new_metric", Type: llama.MetricTypeUntyped, Value: "3.5"}},
	}, "complete", "")
	got := s.runtimeMetricsOverview()
	if got["status"] != "complete" {
		t.Fatalf("status=%v", got["status"])
	}
	samples, ok := got["samples"].([]llama.MetricSample)
	if !ok || len(samples) != 1 || samples[0].Name != "new_metric" {
		t.Fatalf("samples=%#v", got["samples"])
	}
}

func TestRuntimeMetricsOverviewMarksOldSamplesStale(t *testing.T) {
	s := &Server{}
	s.runtimeSnapshotSample = &runtimeSnapshotSample{
		MetricsStatus: "complete",
		MetricsSampledAt: time.Now().Add(-time.Minute),
		Metrics: &llama.MetricsSnapshot{Samples: []llama.MetricSample{{Name: "old_metric", Value: "1"}}},
	}
	got := s.runtimeMetricsOverview()
	if got["status"] != "stale" || got["source_status"] != "complete" {
		t.Fatalf("unexpected stale status: %#v", got)
	}
}


func TestSampleLlamaMetricsMapsRouterModelRequirement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.URL.Query().Get("model") != "" {
			t.Errorf("unexpected request: %s", r.URL.RequestURI())
		}
		http.Error(w, "model name is missing from the request", http.StatusBadRequest)
	}))
	defer srv.Close()

	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	s.sampleLlamaMetrics(context.Background(), llama.New(srv.URL, 0, "llamacpp", ""), "")
	got := s.runtimeSnapshotSampleLatest()
	if got.Status != "complete" || got.MetricsStatus != "needs_model" || got.MetricsError != "router_model_required" {
		t.Fatalf("unexpected metrics capability state: %#v", got)
	}
}

func TestSampleLlamaMetricsDoesNotMapInvalidModelToNeedsModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid model name", http.StatusBadRequest)
	}))
	defer srv.Close()

	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	s.sampleLlamaMetrics(context.Background(), llama.New(srv.URL, 0, "llamacpp", ""), "bad-model")
	got := s.runtimeSnapshotSampleLatest()
	if got.MetricsStatus != "invalid_response" || got.MetricsError != "upstream_bad_request" {
		t.Fatalf("invalid model must not be reported as missing model: %#v", got)
	}
}

func TestApplyCounterDeltasRequiresComparableProcessAndScope(t *testing.T) {
	start := time.Now().Add(-5 * time.Second).UTC()
	end := time.Now().UTC()
	processA, processB := "256533562943", "256533562944"
	previous := &llama.MetricsSnapshot{
		SampledAt: start, ProcessInstanceID: &processA, ModelScope: "model-a",
		Samples: []llama.MetricSample{{Name: "requests_total", Type: llama.MetricTypeCounter, Value: "100", Labels: map[string]string{"route": "/v1"}}},
	}
	makeCurrent := func(value string, process *string, scope string) *llama.MetricsSnapshot {
		return &llama.MetricsSnapshot{
			SampledAt: end, ProcessInstanceID: process, ModelScope: scope,
			Samples: []llama.MetricSample{{Name: "requests_total", Type: llama.MetricTypeCounter, Value: value, Labels: map[string]string{"route": "/v1"}}},
		}
	}

	current := makeCurrent("135", &processA, "model-a")
	applyCounterDeltas(current, previous, "http://llama", "http://llama")
	got := current.Samples[0]
	if got.DeltaStatus != "comparable" || got.DeltaValue != "35" || got.WindowMS <= 0 || got.WindowStart == "" || got.WindowEnd == "" {
		t.Fatalf("expected comparable counter window: %#v", got)
	}

	restarted := makeCurrent("140", &processB, "model-a")
	applyCounterDeltas(restarted, previous, "http://llama", "http://llama")
	if restarted.Samples[0].DeltaReason != "process_restarted" {
		t.Fatalf("process identity change must invalidate delta: %#v", restarted.Samples[0])
	}

	unknownIdentity := makeCurrent("140", nil, "model-a")
	applyCounterDeltas(unknownIdentity, previous, "http://llama", "http://llama")
	if unknownIdentity.Samples[0].DeltaReason != "process_identity_unavailable" {
		t.Fatalf("missing process identity must invalidate delta: %#v", unknownIdentity.Samples[0])
	}

	reset := makeCurrent("90", &processA, "model-a")
	applyCounterDeltas(reset, previous, "http://llama", "http://llama")
	if reset.Samples[0].DeltaReason != "counter_reset" {
		t.Fatalf("counter reset must invalidate delta: %#v", reset.Samples[0])
	}

	scopeChanged := makeCurrent("140", &processA, "model-b")
	applyCounterDeltas(scopeChanged, previous, "http://llama", "http://llama")
	if scopeChanged.Samples[0].DeltaReason != "model_scope_changed" {
		t.Fatalf("model scope change must invalidate delta: %#v", scopeChanged.Samples[0])
	}

	cases := []struct {
		name       string
		mutate     func(*llama.MetricsSnapshot)
		prevTarget string
		curTarget  string
		want       string
	}{
		{name: "upstream changed", prevTarget: "http://old", curTarget: "http://new", want: "upstream_changed"},
		{name: "sampling gap", mutate: func(s *llama.MetricsSnapshot) { s.SampledAt = start.Add(20 * time.Second) }, prevTarget: "http://llama", curTarget: "http://llama", want: "sampling_gap"},
		{name: "invalid window", mutate: func(s *llama.MetricsSnapshot) { s.SampledAt = start }, prevTarget: "http://llama", curTarget: "http://llama", want: "invalid_sampling_window"},
		{name: "missing series", mutate: func(s *llama.MetricsSnapshot) { s.Samples[0].Name = "other_total" }, prevTarget: "http://llama", curTarget: "http://llama", want: "series_missing"},
		{name: "invalid counter", mutate: func(s *llama.MetricsSnapshot) { s.Samples[0].Value = "NaN" }, prevTarget: "http://llama", curTarget: "http://llama", want: "invalid_counter_value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := makeCurrent("140", &processA, "model-a")
			if tc.mutate != nil {
				tc.mutate(current)
			}
			applyCounterDeltas(current, previous, tc.prevTarget, tc.curTarget)
			if current.Samples[0].DeltaStatus != "not_comparable" || current.Samples[0].DeltaReason != tc.want {
				t.Fatalf("expected not_comparable/%s, got %#v", tc.want, current.Samples[0])
			}
		})
	}
}

func TestSampleLlamaMetricsCancellationIsIsolated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	s.sampleLlamaMetrics(ctx, llama.New("http://127.0.0.1:1", 0, "llamacpp", ""), "")
	got := s.runtimeSnapshotSampleLatest()
	if got.Status != "complete" || got.MetricsStatus != "cancelled" || got.MetricsError != "sampling_cancelled" {
		t.Fatalf("metrics cancellation must remain isolated: %#v", got)
	}
}

func TestSampleLlamaMetricsTimeoutIsIsolated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	s := &Server{}
	s.setRuntimeSnapshotSample(nil, "complete", "upstream_runtime")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	s.sampleLlamaMetrics(ctx, llama.New(srv.URL, 0, "llamacpp", ""), "")
	got := s.runtimeSnapshotSampleLatest()
	if got.Status != "complete" || got.MetricsStatus != "timeout" || got.MetricsError != "sampling_timeout" {
		t.Fatalf("metrics timeout must remain isolated: %#v", got)
	}
}


func TestRunRuntimeSnapshotSamplingStartsImmediatelyAndDoesNotOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	done := make(chan struct{})

	go func() {
		defer close(done)
		runRuntimeSnapshotSampling(ctx, func(context.Context) {
			current := active.Add(1)
			for previous := maxActive.Load(); current > previous; previous = maxActive.Load() {
				if maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			calls.Add(1)
			time.Sleep(12 * time.Millisecond)
			active.Add(-1)
		}, 2*time.Millisecond)
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for calls.Load() < 3 {
		select {
		case <-deadline.C:
			cancel()
			<-done
			t.Fatalf("周期采样未按预期执行，calls=%d", calls.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消 Context 后采样循环未退出")
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("采样任务发生重叠，最大并发数=%d", got)
	}
}

func TestRunRuntimeSnapshotSamplingCancellationInterruptsCurrentSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRuntimeSnapshotSampling(ctx, func(sampleCtx context.Context) {
			close(started)
			<-sampleCtx.Done()
		}, time.Millisecond)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("采样循环未执行首次采样")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("当前采样未响应 Context 取消")
	}
}
