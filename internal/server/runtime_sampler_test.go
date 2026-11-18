package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeCPUSamplerStartsImmediatelyAndStops(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := newRuntimeCPUSampler(ctx, func() {
		calls.Add(1)
	})
	if s == nil {
		t.Fatal("采样器不应为空")
	}

	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("采样器未执行首次采样")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := s.stop(stopCtx); err != nil {
		t.Fatalf("停止采样器失败: %v", err)
	}
	count := calls.Load()
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != count {
		t.Fatalf("采样器停止后仍继续执行: stop前=%d stop后=%d", count, got)
	}
}

func TestRuntimeCPUSamplerParentCancellation(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	s := newRuntimeCPUSampler(ctx, func() {
		calls.Add(1)
	})
	cancel()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := s.stop(stopCtx); err != nil {
		t.Fatalf("父 Context 取消后停止采样器失败: %v", err)
	}
	if calls.Load() > 1 {
		t.Fatalf("父 Context 取消后不应继续周期采样: calls=%d", calls.Load())
	}
}
