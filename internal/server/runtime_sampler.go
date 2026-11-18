package server

import (
	"context"
	"sync"
	"time"
)

// runtimeSamplingInterval 控制请求侧可复用运行时快照的刷新频率。
const runtimeSamplingInterval = 5 * time.Second

// runtimeCPUSampler 用于测试和生产代码复用同一生命周期逻辑。
type runtimeCPUSampler struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRuntimeCPUSampler(ctx context.Context, sample func()) *runtimeCPUSampler {
	if ctx == nil {
		ctx = context.Background()
	}
	child, cancel := context.WithCancel(ctx)
	s := &runtimeCPUSampler{ctx: child, cancel: cancel}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		sample()
		ticker := time.NewTicker(runtimeSamplingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return s
}

func (s *runtimeCPUSampler) stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cancel()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startRuntimeSampling 启动低频运行时 CPU 采样器。
func (s *Server) startRuntimeSampling() {
	if s == nil || s.cpuSamplerCancel != nil {
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

// sampleCPU 更新主机 CPU 快照；等待窗口永远发生在后台。
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
		if ok2 && total2 > total {
			td := total2 - total
			if td > 0 {
				s.cpuCachedPercent = clampCPUPercent(100 * float64(td-(idle2-idle)) / float64(td))
			}
			s.cpuLastTotalJiffies = total2
			s.cpuLastIdleJiffies = idle2
			s.cpuProcLastSample = time.Now()
		}
		s.cpuSampleMu.Unlock()
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
