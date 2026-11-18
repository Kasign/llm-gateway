package server

import (
	"context"
	"time"
)

// runtimeSamplingInterval 控制请求侧可复用运行时快照的刷新频率。
// 采样发生在后台，不进入 /v1 请求关键路径。
const runtimeSamplingInterval = 5 * time.Second

// startRuntimeSampling 启动低频运行时采样器。
// 第一阶段只负责 CPU：CPU 首次采样需要时间窗口，因此绝不能由请求路径触发。
func (s *Server) startRuntimeSampling() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cpuSamplerCancel = cancel
	s.cpuSamplerWG.Add(1)
	go func() {
		defer s.cpuSamplerWG.Done()
		s.sampleCPU()
		ticker := time.NewTicker(runtimeSamplingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.sampleCPU()
			}
		}
	}()
}

// sampleCPU 更新主机 CPU 快照。
// 首次采样仍使用现有 200ms 差值窗口，但该等待发生在后台采样器。
func (s *Server) sampleCPU() {
	if s == nil {
		return
	}

	total, idle, ok := readProcStatJiffies()
	if !ok {
		return
	}

	s.cpuSampleMu.Lock()
	if s.cpuProcLastSample.IsZero() {
		s.cpuLastTotalJiffies = total
		s.cpuLastIdleJiffies = idle
		s.cpuProcLastSample = time.Now()
		s.cpuSampleMu.Unlock()

		time.Sleep(200 * time.Millisecond)

		total2, idle2, ok2 := readProcStatJiffies()
		s.cpuSampleMu.Lock()
		defer s.cpuSampleMu.Unlock()
		if !ok2 || total2 <= total {
			return
		}
		totalDelta := total2 - total
		idleDelta := idle2 - idle
		if totalDelta > 0 {
			s.cpuCachedPercent = clampCPUPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
		}
		s.cpuLastTotalJiffies = total2
		s.cpuLastIdleJiffies = idle2
		s.cpuProcLastSample = time.Now()
		return
	}

	totalDelta := total - s.cpuLastTotalJiffies
	idleDelta := idle - s.cpuLastIdleJiffies
	s.cpuLastTotalJiffies = total
	s.cpuLastIdleJiffies = idle
	s.cpuProcLastSample = time.Now()
	if totalDelta > 0 {
		s.cpuCachedPercent = clampCPUPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
	}
	s.cpuSampleMu.Unlock()
}

// cpuSnapshotPercent 返回最近一次后台采样值，不触发新的采样或等待。
func (s *Server) cpuSnapshotPercent() (float64, bool) {
	if s == nil {
		return 0, false
	}
	s.cpuSampleMu.Lock()
	defer s.cpuSampleMu.Unlock()
	if s.cpuProcLastSample.IsZero() {
		return 0, false
	}
	return s.cpuCachedPercent, true
}
