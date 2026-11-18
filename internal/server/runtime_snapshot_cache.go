// server（runtime_snapshot_cache）：本地 Provider 运行时快照与 llama.cpp Metrics 的短期内存缓存。
package server

import (
	"context"
	"errors"
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
	statusCode, body, err := client.FetchMetrics(ctx, modelID, llama.DefaultMetricsResponseLimit)
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
		s.setRuntimeMetricsSample(nil, status, code)
		return
	}
	if statusCode < 200 || statusCode >= 300 {
		switch statusCode {
		case 501:
			s.setRuntimeMetricsSample(nil, "unsupported", "endpoint_disabled_or_unsupported")
		case 400:
			s.setRuntimeMetricsSample(nil, "needs_model", "router_model_required_or_invalid")
		case 401, 403:
			s.setRuntimeMetricsSample(nil, "unavailable", "upstream_auth_failed")
		default:
			s.setRuntimeMetricsSample(nil, "unavailable", "upstream_http_error")
		}
		return
	}
	snapshot, err := llama.ParsePrometheusMetrics(body, sampledAt)
	if err != nil {
		s.setRuntimeMetricsSample(nil, "invalid_response", "metrics_parse_failed")
		return
	}
	s.setRuntimeMetricsSample(snapshot, "complete", "")
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
	s.runtimeSnapshotSample = &runtimeSnapshotSample{Snapshot: copySnap, Status: status, Source: source, SampledAt: time.Now()}
}

func (s *Server) setRuntimeMetricsSample(snapshot *llama.MetricsSnapshot, status, errCode string) {
	if s == nil {
		return
	}
	s.runtimeSnapshotSampleMu.Lock()
	defer s.runtimeSnapshotSampleMu.Unlock()
	if s.runtimeSnapshotSample == nil {
		s.runtimeSnapshotSample = &runtimeSnapshotSample{Status: "unavailable", Source: "runtime_sampler", SampledAt: time.Now()}
	}
	s.runtimeSnapshotSample.Metrics = cloneMetricsSnapshot(snapshot)
	s.runtimeSnapshotSample.MetricsStatus = status
	s.runtimeSnapshotSample.MetricsError = errCode
	s.runtimeSnapshotSample.MetricsSampledAt = time.Now()
}

func cloneMetricsSnapshot(snapshot *llama.MetricsSnapshot) *llama.MetricsSnapshot {
	if snapshot == nil {
		return nil
	}
	out := &llama.MetricsSnapshot{SampledAt: snapshot.SampledAt, Samples: make([]llama.MetricSample, len(snapshot.Samples))}
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
