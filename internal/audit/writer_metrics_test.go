package audit

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

func TestWriterStatsTrackQueuePressureAndDrain(t *testing.T) {
	w := NewWriter(nil, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseWorker()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = w.Shutdown(ctx)
	})

	w.enqueueCritical(func(context.Context, *store.Repo) error {
		close(started)
		<-release
		return nil
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}

	noop := func(context.Context, *store.Repo) error { return nil }
	w.enqueue(noop)
	w.enqueue(noop)
	w.enqueue(noop)
	w.enqueue(noop) // 普通事件不能占用关键任务预留空间。
	w.enqueueCritical(noop) // 使用预留槽位，直到队列真正填满。
	w.enqueueCritical(noop) // 队列已满时，关键任务也只能有界丢弃。
	before := w.Stats()
	if before.QueueDepth != 4 || before.QueueCapacity != 4 || before.NormalLimit != 3 {
		t.Fatalf("queue snapshot = depth/capacity/normal_limit %d/%d/%d, want 4/4/3", before.QueueDepth, before.QueueCapacity, before.NormalLimit)
	}
	if before.EnqueuedTotal != 5 || before.DroppedTotal != 2 || before.AttemptedTotal != 7 {
		t.Fatalf("enqueue/drop totals = %d/%d/%d, want 5/2/7", before.EnqueuedTotal, before.DroppedTotal, before.AttemptedTotal)
	}
	if before.DroppedNormalReservedTotal != 1 || before.DroppedQueueFullTotal != 1 || before.DroppedWriterClosedTotal != 0 {
		t.Fatalf("drop reasons = reserved/full/closed %d/%d/%d, want 1/1/0", before.DroppedNormalReservedTotal, before.DroppedQueueFullTotal, before.DroppedWriterClosedTotal)
	}
	if math.Abs(before.DropRate-float64(2)/7) > 1e-9 {
		t.Fatalf("drop_rate = %f, want 2/7", before.DropRate)
	}

	releaseWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	w.enqueueCritical(noop) // 关闭后的入队应计入 writer_closed，不得向已关闭 channel 发送。
	after := w.Stats()
	if after.QueueDepth != 0 || !after.Closed {
		t.Fatalf("post-shutdown queue depth/closed = %d/%v, want 0/true", after.QueueDepth, after.Closed)
	}
	if after.WriteSucceededTotal != 5 || after.WriteFailedTotal != 0 || after.QueueWaitCount != 5 {
		t.Fatalf("write metrics success/fail/wait_count = %d/%d/%d, want 5/0/5", after.WriteSucceededTotal, after.WriteFailedTotal, after.QueueWaitCount)
	}
	if after.DroppedTotal != 3 || after.DroppedWriterClosedTotal != 1 || math.Abs(after.DropRate-float64(3)/8) > 1e-9 {
		t.Fatalf("post-shutdown drop metrics total/closed/rate = %d/%d/%f, want 3/1/0.375", after.DroppedTotal, after.DroppedWriterClosedTotal, after.DropRate)
	}
}

func TestWriterStatsCountWriteFailuresWithoutExposingErrorText(t *testing.T) {
	w := NewWriter(nil, 8)
	w.enqueueCritical(func(context.Context, *store.Repo) error {
		return errors.New("private database error must not appear in metrics")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	stats := w.Stats()
	if stats.WriteFailedTotal != 1 || stats.WriteSucceededTotal != 0 {
		t.Fatalf("write totals success/fail = %d/%d, want 0/1", stats.WriteSucceededTotal, stats.WriteFailedTotal)
	}
}
