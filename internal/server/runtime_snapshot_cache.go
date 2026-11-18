// server（runtime_snapshot_cache）：Ollama / llama 运行时快照短期共享缓存，避免首页+模型页并行打 /api/tags、/api/ps。
package server

import (
	"context"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

const (
	upstreamRuntimeSnapshotTTL      = 10 * time.Second
	upstreamRuntimeInMemoryCacheTTL = 30 * time.Second
)

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


// sampleLocalRuntimeSnapshot 在后台采集本地 Provider 的运行时状态，不影响推理请求。
func (s *Server) sampleLocalRuntimeSnapshot(ctx context.Context) {
	if s == nil { return }
	s.upstreamMu.RLock()
	upstream := s.upstream
	client := s.llama
	s.upstreamMu.RUnlock()
	backend := strings.ToLower(strings.TrimSpace(upstream.Backend))
	if client == nil || (backend != "ollama" && backend != "llama.cpp" && backend != "llamacpp") {
		s.setRuntimeSnapshotSample(nil, "unavailable", "local_provider")
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
		return
	}
	s.setRuntimeSnapshotSample(snap, "complete", "upstream_runtime")
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

func (s *Server) runtimeSnapshotSampleLatest() *runtimeSnapshotSample {
	if s == nil { return nil }
	s.runtimeSnapshotSampleMu.RLock()
	defer s.runtimeSnapshotSampleMu.RUnlock()
	if s.runtimeSnapshotSample == nil { return nil }
	out := *s.runtimeSnapshotSample
	if s.runtimeSnapshotSample.Snapshot != nil {
		v := *s.runtimeSnapshotSample.Snapshot
		v.Loaded = append([]llama.CatalogItem(nil), v.Loaded...)
		v.Catalog = append([]llama.CatalogItem(nil), v.Catalog...)
		out.Snapshot = &v
	}
	return &out
}
