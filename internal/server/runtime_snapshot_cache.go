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
