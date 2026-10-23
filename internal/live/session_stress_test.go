package live

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

// TestSessionConcurrentUpdatesAndSnapshotsStayBounded 压测多生产者更新同一条
// Live Session 时的事件上限、快照复制及标量读取；race detector 验证锁边界。
func TestSessionConcurrentUpdatesAndSnapshotsStayBounded(t *testing.T) {
	const (
		workerCount   = 12
		eventsPerWorker = 750
		requestID     = "live-session-concurrent-stress"
	)
	registry := NewRegistry()
	session := registry.Register(store.RequestLog{
		RequestID: requestID,
		Status:    "running",
		StartedAt: time.Now(),
	})
	if session == nil {
		t.Fatal("session was not registered")
	}
	defer registry.Unregister(requestID)

	var stop atomic.Bool
	var snapshotCount atomic.Uint64
	var invalidSnapshot atomic.Bool
	snapshotStarted := make(chan struct{})
	snapshotDone := make(chan struct{})
	go func() {
		close(snapshotStarted)
		defer close(snapshotDone)
		for {
			snapshot := registry.SnapshotDetail(requestID)
			if snapshot == nil || len(snapshot.Events) > maxStreamEvents ||
				len(snapshot.ReqBody) > maxBodyBytes || len(snapshot.ResBody) > maxBodyBytes {
				invalidSnapshot.Store(true)
				return
			}
			if got := len(registry.ListSummaries()); got > 1 {
				invalidSnapshot.Store(true)
				return
			}
			snapshotCount.Add(1)
			if stop.Load() {
				return
			}
		}
	}()
	<-snapshotStarted

	var producers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		worker := worker
		producers.Add(1)
		go func() {
			defer producers.Done()
			for i := 0; i < eventsPerWorker; i++ {
				seq := worker*eventsPerWorker + i + 1
				session.AppendStreamEvent(store.StreamEvent{
					Seq: seq, AtMS: int64(i), Kind: "delta", Field: "content", DeltaLen: 1,
					Preview: "test-content",
				})
				ttft, in, out, total := int64(seq), int64(worker+1), int64(i+1), int64(worker+i+2)
				session.UpdateStreamScalars(int64(seq), &ttft, &in, &out, &total)
			}
		}()
	}
	producers.Wait()
	stop.Store(true)
	<-snapshotDone

	if invalidSnapshot.Load() {
		t.Fatal("concurrent detail/list snapshot exceeded a documented memory/event bound")
	}
	if got := snapshotCount.Load(); got == 0 {
		t.Fatal("snapshot goroutine did not observe the session during the stress run")
	}
	if got := len(session.StreamEventsCopy()); got != maxStreamEvents {
		t.Fatalf("retained stream events = %d, want cap %d", got, maxStreamEvents)
	}
	snapshot := registry.SnapshotDetail(requestID)
	if snapshot == nil || snapshot.Log.ResponseBytes <= 0 || snapshot.Log.TTFTMS == nil ||
		snapshot.Log.InputTokens == nil || snapshot.Log.OutputTokens == nil || snapshot.Log.TotalTokens == nil {
		t.Fatalf("latest stream scalars were not retained in the final snapshot: %+v", snapshot)
	}
}

// TestSessionCapsRetainedBodiesAtLimit 验证即使调用方一次性传入超限数据，
// 实时请求详情中的请求/响应正文也不会超过单项内存上限。
func TestSessionCapsRetainedBodiesAtLimit(t *testing.T) {
	session := &Session{}
	oversized := make([]byte, maxBodyBytes+1024)
	session.SetReqBody(oversized)
	session.SetResBody(oversized)
	session.AppendResBody(oversized, true)

	snapshot := session.SnapshotDetail()
	if got := len(snapshot.ReqBody); got != maxBodyBytes {
		t.Fatalf("retained request body = %d bytes, want %d", got, maxBodyBytes)
	}
	if got := len(snapshot.ResBody); got != maxBodyBytes {
		t.Fatalf("retained response body = %d bytes, want %d", got, maxBodyBytes)
	}
}
