// server（runtime_snapshot_cache）：本地 Provider 运行时快照与 llama.cpp Metrics 的短期内存缓存。
package server

import (
	"context"
	"encoding/json"
	"errors"
		"math/big"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

const (
	upstreamRuntimeSnapshotTTL      = 10 * time.Second
	upstreamRuntimeInMemoryCacheTTL = 30 * time.Second
	llamaMetricsSamplingTimeout     = 2 * time.Second
)

type runtimeSnapshotSample struct {
	Snapshot         *llama.RuntimeSnapshot
	Status           string
	Source           string
	SampledAt        time.Time
	Metrics          *llama.MetricsSnapshot
	MetricsStatus    string
	MetricsError     string
	MetricsSampledAt time.Time
	MetricsTarget     string
}

type upstreamRuntimeSnapEntry struct {
	at   time.Time
	snap *llama.RuntimeSnapshot
	err  error
}

func (s *Server) upstreamRuntimeSnapshot(ctx context.Context, backend string, client *llama.Client, active string) (*llama.RuntimeSnapshot, error) {
	return s.upstreamRuntimeSnapshotScoped(ctx, backend, client, active, false)
}

func (s *Server) upstreamRuntimeSnapshotInMemory(ctx context.Context, backend string, client *llama.Client, active string) (*llama.RuntimeSnapshot, error) {
	return s.upstreamRuntimeSnapshotScoped(ctx, backend, client, active, true)
}

func (s *Server) invalidateRuntimeSnapKey(key string) {
	s.runtimeSnapMu.Lock()
	delete(s.runtimeSnapCache, key)
	s.runtimeSnapMu.Unlock()
}

func (s *Server) upstreamRuntimeSnapshotScoped(ctx context.Context, backend string, client *llama.Client, active string, memoryOnly bool) (*llama.RuntimeSnapshot, error) {
	scope := "full"
	ttl := upstreamRuntimeSnapshotTTL
	if memoryOnly {
		scope = "memory"
		ttl = upstreamRuntimeInMemoryCacheTTL
	}
	key := scope + "\x00" + strings.ToLower(strings.TrimSpace(backend)) + "\x00" + strings.TrimSpace(client.BaseURL())

	s.runtimeSnapMu.Lock()
	if s.runtimeSnapCache == nil {
		s.runtimeSnapCache = make(map[string]upstreamRuntimeSnapEntry)
	}
	e, cached := s.runtimeSnapCache[key]
	cacheValid := cached && time.Since(e.at) < ttl
	s.runtimeSnapMu.Unlock()

	if cacheValid {
		if e.err == nil && e.snap != nil {
			return e.snap, nil
		}
		if e.err != nil {
			s.invalidateRuntimeSnapKey(key)
		}
	}

	var snap *llama.RuntimeSnapshot
	var err error
	if memoryOnly {
		snap, err = client.RuntimeSnapshotInMemoryOnly(ctx, active)
	} else {
		snap, err = client.RuntimeSnapshot(ctx, active)
	}
	if err == nil && snap != nil {
		snap.SampledAt = time.Now()
	}

	s.runtimeSnapMu.Lock()
	s.runtimeSnapCache[key] = upstreamRuntimeSnapEntry{at: time.Now(), snap: snap, err: err}
	s.runtimeSnapMu.Unlock()
	return snap, err
}

func (s *Server) invalidateUpstreamRuntimeSnapshotCache() {
	s.runtimeSnapMu.Lock()
	s.runtimeSnapCache = nil
	s.runtimeSnapMu.Unlock()
}

// sampleLocalRuntimeSnapshot 采集本地 Provider 状态；Metrics 失败不影响模型目录快照。
func (s *Server) sampleLocalRuntimeSnapshot(ctx context.Context) {
	if s == nil {
		return
	}
	s.upstreamMu.RLock()
	upstream := s.upstream
	client := s.llama
	s.upstreamMu.RUnlock()
	backend := strings.ToLower(strings.TrimSpace(upstream.Backend))
	if client == nil || (backend != "ollama" && backend != "llama.cpp" && backend != "llamacpp") {
		s.setRuntimeSnapshotSample(nil, "unavailable", "local_provider")
		s.setRuntimeMetricsSample(nil, "skipped", "backend_not_llamacpp")
		return
	}

	var snap *llama.RuntimeSnapshot
	var err error
	if backend == "ollama" {
		snap, err = s.upstreamRuntimeSnapshotInMemory(ctx, backend, client, "")
	} else {
		snap, err = s.upstreamRuntimeSnapshot(ctx, backend, client, "")
	}
	if err != nil || snap == nil {
		s.setRuntimeSnapshotSample(nil, "unavailable", "upstream_runtime")
	} else {
		s.setRuntimeSnapshotSample(snap, "complete", "upstream_runtime")
	}

	if backend == "ollama" {
		s.setRuntimeMetricsSample(nil, "skipped", "backend_not_llamacpp")
		return
	}
	s.sampleLlamaMetrics(ctx, client, upstream.ModelName)
}

// sampleLlamaMetrics 独立采集 Metrics；超时和端点故障不会传播到推理或 RuntimeSnapshot。
func (s *Server) sampleLlamaMetrics(parent context.Context, client *llama.Client, modelID string) {
	ctx, cancel := context.WithTimeout(parent, llamaMetricsSamplingTimeout)
	defer cancel()

	sampledAt := time.Now().UTC()
	statusCode, body, processInstanceID, err := client.FetchMetrics(ctx, modelID, llama.DefaultMetricsResponseLimit)
	if err != nil {
		status := "unavailable"
		code := "request_failed"
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			status, code = "timeout", "sampling_timeout"
		case errors.Is(err, context.Canceled):
			status, code = "cancelled", "sampling_cancelled"
		case errors.Is(err, llama.ErrMetricsResponseTooLarge):
			status, code = "invalid_response", "response_too_large"
		}
		s.setRuntimeMetricsSampleForTarget(nil, status, code, client.BaseURL())
		return
	}
	if statusCode < 200 || statusCode >= 300 {
		switch statusCode {
		case 501:
			s.setRuntimeMetricsSampleForTarget(nil, "unsupported", "endpoint_disabled_or_unsupported", client.BaseURL())
		case 400:
			message := strings.ToLower(strings.TrimSpace(string(body)))
			missingModel := strings.TrimSpace(modelID) == "" &&
				strings.Contains(message, "model") &&
				(strings.Contains(message, "missing") || strings.Contains(message, "required"))
			if missingModel {
				s.setRuntimeMetricsSampleForTarget(nil, "needs_model", "router_model_required", client.BaseURL())
			} else {
				s.setRuntimeMetricsSampleForTarget(nil, "invalid_response", "upstream_bad_request", client.BaseURL())
			}
		case 401, 403:
			s.setRuntimeMetricsSampleForTarget(nil, "unavailable", "upstream_auth_failed", client.BaseURL())
		default:
			s.setRuntimeMetricsSampleForTarget(nil, "unavailable", "upstream_http_error", client.BaseURL())
		}
		return
	}
	snapshot, err := llama.ParsePrometheusMetrics(body, sampledAt)
	if err != nil {
		s.setRuntimeMetricsSampleForTarget(nil, "invalid_response", "metrics_parse_failed", client.BaseURL())
		return
	}
	snapshot.ProcessInstanceID = processInstanceID
	snapshot.ModelScope = strings.TrimSpace(modelID)
	s.setRuntimeMetricsSampleForTarget(snapshot, "complete", "", client.BaseURL())
}

func (s *Server) setRuntimeSnapshotSample(snap *llama.RuntimeSnapshot, status, source string) {
	s.runtimeSnapshotSampleMu.Lock()
	defer s.runtimeSnapshotSampleMu.Unlock()
	var copySnap *llama.RuntimeSnapshot
	if snap != nil {
		v := *snap
		v.Loaded = append([]llama.CatalogItem(nil), snap.Loaded...)
		v.Catalog = append([]llama.CatalogItem(nil), snap.Catalog...)
		copySnap = &v
	}
	// RuntimeSnapshot 每轮先更新；保留上一轮 Metrics，供紧接着的 Metrics 采样计算窗口增量。
	previous := s.runtimeSnapshotSample
	sampledAt := time.Now()
	if snap != nil && !snap.SampledAt.IsZero() {
		sampledAt = snap.SampledAt
	}
	next := &runtimeSnapshotSample{Snapshot: copySnap, Status: status, Source: source, SampledAt: sampledAt}
	if previous != nil {
		next.Metrics = previous.Metrics
		next.MetricsStatus = previous.MetricsStatus
		next.MetricsError = previous.MetricsError
		next.MetricsSampledAt = previous.MetricsSampledAt
		next.MetricsTarget = previous.MetricsTarget
	}
	s.runtimeSnapshotSample = next
}

func (s *Server) setRuntimeMetricsSample(snapshot *llama.MetricsSnapshot, status, errCode string) {
	s.setRuntimeMetricsSampleForTarget(snapshot, status, errCode, "")
}

func (s *Server) setRuntimeMetricsSampleForTarget(snapshot *llama.MetricsSnapshot, status, errCode, target string) {
	if s == nil {
		return
	}
	s.runtimeSnapshotSampleMu.Lock()
	defer s.runtimeSnapshotSampleMu.Unlock()
	if s.runtimeSnapshotSample == nil {
		s.runtimeSnapshotSample = &runtimeSnapshotSample{Status: "unavailable", Source: "runtime_sampler", SampledAt: time.Now()}
	}
	previous := s.runtimeSnapshotSample.Metrics
	previousTarget := s.runtimeSnapshotSample.MetricsTarget
	if snapshot != nil {
		if s.runtimeSnapshotSample.MetricsStatus == "complete" {
			applyCounterDeltas(snapshot, previous, previousTarget, target)
		} else {
			applyCounterDeltas(snapshot, nil, "", target)
		}
	}
	s.runtimeSnapshotSample.Metrics = cloneMetricsSnapshot(snapshot)
	s.runtimeSnapshotSample.MetricsStatus = status
	s.runtimeSnapshotSample.MetricsError = errCode
	s.runtimeSnapshotSample.MetricsSampledAt = time.Now()
	s.runtimeSnapshotSample.MetricsTarget = target
}

func applyCounterDeltas(current, previous *llama.MetricsSnapshot, previousTarget, currentTarget string) {
	if current == nil {
		return
	}
	previousSeries := make(map[string]llama.MetricSample)
	if previous != nil {
		for _, sample := range previous.Samples {
			previousSeries[metricSeriesKey(sample)] = sample
		}
	}
	for i := range current.Samples {
		sample := &current.Samples[i]
		if sample.Type != llama.MetricTypeCounter {
			continue
		}
		sample.DeltaStatus = "not_comparable"
		sample.DeltaReason = "no_previous_sample"
		if previous == nil {
			continue
		}
		if previousTarget == "" || currentTarget == "" || previousTarget != currentTarget {
			sample.DeltaReason = "upstream_changed"
			continue
		}
		if previous.ModelScope != current.ModelScope {
			sample.DeltaReason = "model_scope_changed"
			continue
		}
		if previous.ProcessInstanceID == nil || current.ProcessInstanceID == nil {
			sample.DeltaReason = "process_identity_unavailable"
			continue
		}
		if *previous.ProcessInstanceID != *current.ProcessInstanceID {
			sample.DeltaReason = "process_restarted"
			continue
		}
		start, end := previous.SampledAt, current.SampledAt
		if start.IsZero() || end.IsZero() || !end.After(start) {
			sample.DeltaReason = "invalid_sampling_window"
			continue
		}
		window := end.Sub(start)
		if window > 3*runtimeSnapshotSamplingInterval {
			sample.DeltaReason = "sampling_gap"
			continue
		}
		old, ok := previousSeries[metricSeriesKey(*sample)]
		if !ok {
			sample.DeltaReason = "series_missing"
			continue
		}
		if old.Type != llama.MetricTypeCounter {
			sample.DeltaReason = "metric_type_changed"
			continue
		}
		oldValue, oldOK := new(big.Rat).SetString(old.Value)
		newValue, newOK := new(big.Rat).SetString(sample.Value)
		if !oldOK || !newOK || oldValue.Sign() < 0 || newValue.Sign() < 0 {
			sample.DeltaReason = "invalid_counter_value"
			continue
		}
		if newValue.Cmp(oldValue) < 0 {
			sample.DeltaReason = "counter_reset"
			continue
		}
		delta := new(big.Rat).Sub(newValue, oldValue)
		sample.DeltaStatus = "comparable"
		sample.DeltaReason = ""
		sample.DeltaValue = strings.TrimRight(strings.TrimRight(delta.FloatString(18), "0"), ".")
		if sample.DeltaValue == "" {
			sample.DeltaValue = "0"
		}
		sample.WindowStart = start.UTC().Format(time.RFC3339Nano)
		sample.WindowEnd = end.UTC().Format(time.RFC3339Nano)
		sample.WindowMS = window.Milliseconds()
	}
}

func metricSeriesKey(sample llama.MetricSample) string {
	labels, _ := json.Marshal(sample.Labels)
	return sample.Name + "\x00" + string(labels)
}

func (s *Server) runtimeMetricsOverview() map[string]interface{} {
	out := map[string]interface{}{
		"status": "unavailable",
		"reason_code": "not_sampled",
		"samples": []llama.MetricSample{},
	}
	if s == nil {
		return out
	}
	sample := s.runtimeSnapshotSampleLatest()
	if sample == nil {
		return out
	}
	activeTarget := ""
	if s.cfg != nil {
		activeTarget = strings.TrimSpace(s.baseURLForBackend("llamacpp"))
	}
	if sample.MetricsTarget != "" && activeTarget != "" && sample.MetricsTarget != activeTarget {
		out["reason_code"] = "upstream_changed"
		return out
	}
	status := strings.TrimSpace(sample.MetricsStatus)
	if status == "" {
		status = "unavailable"
	}
	sampledAt := sample.MetricsSampledAt
	if sample.Metrics != nil && !sample.Metrics.SampledAt.IsZero() {
		sampledAt = sample.Metrics.SampledAt
	}
	if sampledAt.IsZero() {
		out["status"] = status
		if sample.MetricsError != "" {
			out["reason_code"] = sample.MetricsError
		}
		return out
	}
	age := time.Since(sampledAt)
	if age < 0 {
		age = 0
	}
	sourceStatus := status
	if age > runtimeSnapshotMaxAge {
		status = "stale"
	}
	out["status"] = status
	out["source"] = "llama_cpp_server_metrics"
	out["sampled_at"] = sampledAt.UTC().Format(time.RFC3339Nano)
	out["age_ms"] = age.Milliseconds()
	if sample.Metrics != nil {
		if sample.Metrics.ProcessInstanceID != nil {
			out["process_instance_id"] = *sample.Metrics.ProcessInstanceID
		}
		if sample.Metrics.ModelScope != "" {
			out["model_scope"] = sample.Metrics.ModelScope
		}
	}
	if status == "stale" {
		out["source_status"] = sourceStatus
	}
	if sample.MetricsError != "" {
		out["reason_code"] = sample.MetricsError
	}
	if sample.Metrics != nil {
		out["samples"] = sample.Metrics.Samples
	}
	return out
}

func cloneMetricsSnapshot(snapshot *llama.MetricsSnapshot) *llama.MetricsSnapshot {
	if snapshot == nil {
		return nil
	}
	out := &llama.MetricsSnapshot{
		SampledAt: snapshot.SampledAt,
		ProcessInstanceID: snapshot.ProcessInstanceID,
		ModelScope: snapshot.ModelScope,
		Samples: make([]llama.MetricSample, len(snapshot.Samples)),
	}
	if snapshot.ProcessInstanceID != nil {
		processStart := *snapshot.ProcessInstanceID
		out.ProcessInstanceID = &processStart
	}
	for i, sample := range snapshot.Samples {
		out.Samples[i] = sample
		if sample.Labels != nil {
			out.Samples[i].Labels = make(map[string]string, len(sample.Labels))
			for key, value := range sample.Labels {
				out.Samples[i].Labels[key] = value
			}
		}
		if sample.Timestamp != nil {
			ts := *sample.Timestamp
			out.Samples[i].Timestamp = &ts
		}
	}
	return out
}

func (s *Server) runtimeSnapshotSampleLatest() *runtimeSnapshotSample {
	if s == nil {
		return nil
	}
	s.runtimeSnapshotSampleMu.RLock()
	defer s.runtimeSnapshotSampleMu.RUnlock()
	if s.runtimeSnapshotSample == nil {
		return nil
	}
	out := *s.runtimeSnapshotSample
	if s.runtimeSnapshotSample.Snapshot != nil {
		v := *s.runtimeSnapshotSample.Snapshot
		v.Loaded = append([]llama.CatalogItem(nil), v.Loaded...)
		v.Catalog = append([]llama.CatalogItem(nil), v.Catalog...)
		out.Snapshot = &v
	}
	out.Metrics = cloneMetricsSnapshot(s.runtimeSnapshotSample.Metrics)
	return &out
}
