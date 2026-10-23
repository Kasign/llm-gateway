package audit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

// TestWriterQueueRemainsBoundedUnderConcurrentPressure 锁住唯一 Worker，
// 让并发生产者确定性地填满普通队列并验证关键任务预留容量与丢弃计数。
func TestWriterQueueRemainsBoundedUnderConcurrentPressure(t *testing.T) {
	const (
		queueSize      = 64
		producerCount  = 12
		jobsPerProducer = 500
	)
	w := NewWriter(nil, queueSize)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseWorker()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = w.Shutdown(ctx)
	})

	var executed atomic.Int64
	w.enqueueCritical(func(context.Context, *store.Repo) error {
		close(started)
		<-release
		executed.Add(1)
		return nil
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start the blocking job")
	}

	noop := func(context.Context, *store.Repo) error {
		executed.Add(1)
		return nil
	}
	var producers sync.WaitGroup
	for i := 0; i < producerCount; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < jobsPerProducer; j++ {
				w.enqueue(noop)
			}
		}()
	}
	producers.Wait()

	totalNormal := producerCount * jobsPerProducer
	if got := len(w.ch); got != w.normalLimit {
		t.Fatalf("queued normal jobs = %d, want reserved boundary %d (capacity=%d)", got, w.normalLimit, cap(w.ch))
	}
	wantDropped := uint64(totalNormal - w.normalLimit)
	if got := w.dropped.Load(); got != wantDropped {
		t.Fatalf("dropped normal jobs = %d, want %d", got, wantDropped)
	}

	// 普通任务不能占用的槽位仍可被关键任务使用，直到整个 channel 真正填满。
	reserved := cap(w.ch) - len(w.ch)
	if reserved != reservedAuditCapacity(queueSize) {
		t.Fatalf("remaining capacity = %d, want reserved capacity %d", reserved, reservedAuditCapacity(queueSize))
	}
	for i := 0; i < reserved; i++ {
		w.enqueueCritical(noop)
	}
	if got := len(w.ch); got != queueSize {
		t.Fatalf("queued jobs after critical writes = %d, want full queue %d", got, queueSize)
	}
	w.enqueueCritical(noop) // 真正满载后，关键任务也必须有限界地丢弃，不能阻塞推理调用。
	if got := w.dropped.Load(); got != wantDropped+1 {
		t.Fatalf("dropped after queue saturation = %d, want %d", got, wantDropped+1)
	}

	releaseWorker()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown after pressure test: %v", err)
	}
	if got, want := executed.Load(), int64(queueSize+1); got != want {
		t.Fatalf("executed jobs = %d, want %d (one running + full queue)", got, want)
	}
	if got := len(w.ch); got != 0 {
		t.Fatalf("queue not drained after shutdown: %d jobs remain", got)
	}
}
