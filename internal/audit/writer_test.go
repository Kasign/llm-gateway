package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

func TestWriterRejectsOperationsAfterShutdown(t *testing.T) {
	w := NewWriter(nil, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// 关闭后继续入队应该安全丢弃，不能向已关闭的 channel 发送并 panic。
	w.enqueue(func(context.Context, *store.Repo) error { return nil })
	if err := w.Flush(ctx); !errors.Is(err, errWriterClosed) {
		t.Fatalf("Flush after Shutdown error = %v, want %v", err, errWriterClosed)
	}
}

func TestWriterConcurrentEnqueueAndShutdown(t *testing.T) {
	w := NewWriter(nil, 8)
	var producers sync.WaitGroup
	for i := 0; i < 8; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 1000; j++ {
				w.enqueue(func(context.Context, *store.Repo) error { return nil })
			}
		}()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- w.Shutdown(ctx)
	}()

	producers.Wait()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("concurrent shutdown: %v", err)
	}
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
}
