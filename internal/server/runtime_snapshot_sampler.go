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
		runRuntimeSnapshotSampling(ctx, s.sampleLocalRuntimeSnapshot, runtimeSnapshotSamplingInterval)
	}()
}

// runRuntimeSnapshotSampling 立即执行首次采样，再按固定周期串行采样。
// sample 接收生命周期 Context，关闭时可取消正在执行的上游请求。
func runRuntimeSnapshotSampling(ctx context.Context, sample func(context.Context), interval time.Duration) {
	if ctx == nil || sample == nil {
		return
	}
	if interval <= 0 {
		interval = runtimeSnapshotSamplingInterval
	}
	sample(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample(ctx)
		}
	}
}
