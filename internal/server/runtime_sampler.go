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
		// 首次采样异步执行，避免启动流程和请求路径等待 200ms。
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
	now := time.Now()
	s.cpuSampleMu.Lock()
	defer s.cpuSampleMu.Unlock()
	if s.cpuProcLastSample.IsZero() {
		s.cpuLastTotalJiffies = total
		s.cpuLastIdleJiffies = idle
		s.cpuProcLastSample = now
		s.cpuSampleMu.Unlock()
		time.Sleep(200 * time.Millisecond)
		total2, idle2, ok2 := readProcStatJiffies()
		s.cpuSampleMu.Lock()
		if ok2 && total2 > total {
			td := total2 - total
			if td > 0 {
				s.cpuCachedPercent = clampCPUPercent(100 * float64(td-(idle2-idle)) / float64(td))
			}
			s.cpuLastTotalJiffies = total2
			s.cpuLastIdleJiffies = idle2
			s.cpuProcLastSample = time.Now()
		}
		return
	}
	totalDelta := total - s.cpuLastTotalJiffies
	idleDelta := idle - s.cpuLastIdleJiffies
	s.cpuLastTotalJiffies = total
	s.cpuLastIdleJiffies = idle
	s.cpuProcLastSample = now
	if totalDelta > 0 {
		s.cpuCachedPercent = clampCPUPercent(100 * float64(totalDelta-idleDelta) / float64(totalDelta))
	}
}

// cpuSnapshotPercent 返回最近一次后台采样值，不触发新的采样或等待。
// 因此请求分析读取该值不会增加固定延迟。
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
