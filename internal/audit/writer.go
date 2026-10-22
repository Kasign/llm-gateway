// audit 在后台异步写入调用审计，避免 MariaDB IO 占用推理与 SSE 转发 goroutine。
package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/store"
)

const (
	defaultQueueSize = 16384
	workerCount      = 1 // 单 worker 保证同一 request 的 Create → Finish 顺序
)

type job func(context.Context, *store.Repo) error

// Writer 有界队列 + 后台 worker；队列满时丢弃任务并计数，不影响 HTTP 响应。
type Writer struct {
	repo    *store.Repo
	ch      chan job
	wg      sync.WaitGroup
	dropped atomic.Uint64

	// mu 同步发送与关闭，防止 Shutdown 关闭 channel 时并发 enqueue/Flush 触发 panic。
	mu     sync.RWMutex
	closed bool
}

var errWriterClosed = errors.New("audit writer is shut down")

func NewWriter(repo *store.Repo, queueSize int) *Writer {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	w := &Writer{
		repo: repo,
		ch:   make(chan job, queueSize),
	}
	w.wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go w.loop()
	}
	return w
}

func (w *Writer) loop() {
	defer w.wg.Done()
	for fn := range w.ch {
		if fn == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		err := fn(ctx, w.repo)
		cancel()
		if err != nil {
			applog.Printf("audit write failed: %v", err)
		}
	}
}

func (w *Writer) enqueue(fn job) {
	if fn == nil {
		return
	}
	// 入队使用读锁，Shutdown 需要写锁才能关闭 channel，避免并发向已关闭 channel 发送。
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		w.recordDrop("writer_closed")
		return
	}
	select {
	case w.ch <- fn:
		w.mu.RUnlock()
	default:
		w.mu.RUnlock()
		w.recordDrop("queue_full")
	}
}

func (w *Writer) recordDrop(reason string) {
	n := w.dropped.Add(1)
	if n == 1 || n%1000 == 0 {
		applog.Printf("audit enqueue dropped: reason=%s total=%d", reason, n)
	}
}

func (w *Writer) CreateRequest(l store.RequestLog) {
	rec := l
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.CreateRequest(ctx, rec)
	})
}

func (w *Writer) UpdateRequestBytes(requestID string, n int64) {
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.UpdateRequestBytes(ctx, requestID, n)
	})
}

func (w *Writer) PatchRequestAfterParse(requestID, modelRequested string, stream bool) {
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.PatchRequestAfterParse(ctx, requestID, modelRequested, stream)
	})
}

func (w *Writer) PatchRequestModelActual(requestID, modelActual string) {
	if strings.TrimSpace(modelActual) == "" {
		return
	}
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.PatchRequestModelActual(ctx, requestID, modelActual)
	})
}

func (w *Writer) FinishRequest(l store.RequestLog) {
	rec := l
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.FinishRequest(ctx, rec)
	})
}

func (w *Writer) SaveRequestAnalysis(requestID, analysisJSON string) {
	if analysisJSON == "" {
		return
	}
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.SaveRequestAnalysis(ctx, requestID, analysisJSON)
	})
}

func (w *Writer) SavePayloadHeaders(requestID, clientJSON, upstreamJSON string) {
	if requestID == "" || (clientJSON == "" && upstreamJSON == "") {
		return
	}
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.SavePayloadHeaders(ctx, requestID, clientJSON, upstreamJSON)
	})
}

func (w *Writer) SavePayload(requestID string, reqBody, resBody []byte) {
	if len(reqBody) == 0 && len(resBody) == 0 {
		return
	}
	reqCopy := append([]byte(nil), reqBody...)
	resCopy := append([]byte(nil), resBody...)
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.SavePayload(ctx, requestID, reqCopy, resCopy)
	})
}

func (w *Writer) InsertStreamEvents(requestID string, events []store.StreamEvent) {
	if len(events) == 0 {
		return
	}
	evCopy := append([]store.StreamEvent(nil), events...)
	w.enqueue(func(ctx context.Context, r *store.Repo) error {
		return r.InsertStreamEvents(ctx, requestID, evCopy)
	})
}

// Shutdown 停止接收新任务并关闭队列，worker 会按队列顺序处理已入队任务。
// 即使 ctx 超时，队列也保持关闭状态；后续 enqueue 会计数丢弃，不会向已关闭 channel 发送。
func (w *Writer) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
	w.mu.Unlock()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush 等待当前已入队任务处理完（不关闭 writer），供 benchmark 等需要立即可查库的场景。
func (w *Writer) Flush(ctx context.Context) error {
	done := make(chan struct{})
	fn := func(context.Context, *store.Repo) error {
		close(done)
		return nil
	}

	// 发送哨兵与 Shutdown 共用生命周期锁，避免 Flush 对已关闭 channel 发送。
	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return errWriterClosed
	}
	select {
	case w.ch <- fn:
		w.mu.RUnlock()
	case <-ctx.Done():
		w.mu.RUnlock()
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
