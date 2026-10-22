package audit

import (
	"context"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

func TestWriterReservesQueueCapacityForCriticalJobs(t *testing.T) {
	w := NewWriter(nil, 4)
	started := make(chan struct{})
	release := make(chan struct{})
	w.enqueueCritical(func(context.Context, *store.Repo) error {
		close(started)
		<-release
		return nil
	})
	<-started // 阻塞 Worker，让后续入队边界保持确定。

	noop := func(context.Context, *store.Repo) error { return nil }
	w.enqueue(noop)
	w.enqueue(noop)
	w.enqueue(noop)
	w.enqueue(noop) // 普通任务达到 normalLimit 后应被丢弃。
	w.enqueueCritical(noop) // 预留容量仍允许关键任务进入同一个 FIFO 队列。
	if got := w.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1 normal task dropped", got)
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
