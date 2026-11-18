package server

import (
	"context"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

type runtimeSnapshotSample struct {
	Snapshot   *llama.RuntimeSnapshot
	Status     string
	Source     string
	SampledAt  time.Time
}

// sampleLocalRuntimeSnapshot 在后台读取本地 Provider 的最近运行时状态。
// 不在请求路径主动访问上游；失败只影响观测状态，不影响推理请求。
func (s *Server) sampleLocalRuntimeSnapshot(ctx context.Context) {
	if s == nil {
		return
	}
	s.upstreamMu.RLock()
	upstream := s.upstream
	client := s.llama
	s.upstreamMu.RUnlock()

	backend := strings.ToLower(strings.TrimSpace(upstream.Backend))
	if !isLocalInferenceBackend(backend) || client == nil {
		s.setRuntimeSnapshotSample(nil, "unavailable", "local_provider")
		return
	}

	var (
		snap *llama.RuntimeSnapshot
		err  error
	)
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
	s.runtimeSnapshotSample = &runtimeSnapshotSample{
		Snapshot: copySnap,
		Status: status,
		Source: source,
		SampledAt: time.Now(),
	}
}

// runtimeSnapshotSampleLatest 返回不可变副本，避免请求侧读到后台更新中的切片。
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
	return &out
}
