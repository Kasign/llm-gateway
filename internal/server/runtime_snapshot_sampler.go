package server

import (
	"context"
	"time"
)

const runtimeSnapshotSamplingInterval = 5 * time.Second

// startRuntimeSnapshotSampling 独立于 CPU sampler，避免上游运行时查询阻塞 CPU 采样。
func (s *Server) startRuntimeSnapshotSampling() {
	if s == nil || s.runtimeSnapshotSamplerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.runtimeSnapshotSamplerCancel = cancel
	s.runtimeSnapshotSamplerWG.Add(1)
	go func() {
		defer s.runtimeSnapshotSamplerWG.Done()
		s.sampleLocalRuntimeSnapshot(ctx)
		ticker := time.NewTicker(runtimeSnapshotSamplingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sampleLocalRuntimeSnapshot(ctx)
			}
		}
	}()
}
