package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/core"
)

func waitForConcurrencyQueueDepth(t *testing.T, limiter *ConcurrencyLimiter, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if limiter.QueueDepth() == want { return }
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth = %d, want %d", limiter.QueueDepth(), want)
}

func TestConcurrencyLimiterQueueDispatchesInFIFOOrder(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil { t.Fatal(err) }
	if err := limiter.SetQueueLimit(2); err != nil { t.Fatal(err) }
	if !limiter.TryAcquire() { t.Fatal("expected initial active request to acquire the slot") }

	acquired := make(chan int, 2)
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		err := limiter.Acquire(context.Background(), 3*time.Second)
		if err == nil { acquired <- 1; <-releaseFirst; limiter.Release() }
		done <- err
	}()
	waitForConcurrencyQueueDepth(t, limiter, 1)
	go func() {
		err := limiter.Acquire(context.Background(), 3*time.Second)
		if err == nil { acquired <- 2; <-releaseSecond; limiter.Release() }
		done <- err
	}()
	waitForConcurrencyQueueDepth(t, limiter, 2)

	limiter.Release()
	select {
	case id := <-acquired:
		if id != 1 { t.Fatalf("first granted request ID = %d, want FIFO request 1", id) }
	case <-time.After(2*time.Second): t.Fatal("first queued request was not granted")
	}
	close(releaseFirst)
	select {
	case id := <-acquired:
		if id != 2 { t.Fatalf("second granted request ID = %d, want FIFO request 2", id) }
	case <-time.After(2*time.Second): t.Fatal("second queued request was not granted")
	}
	close(releaseSecond)
	for i:=0;i<2;i++ { if err:=<-done; err!=nil { t.Fatalf("queued request failed: %v",err) } }
	if got:=limiter.Active();got!=0 { t.Fatalf("active slots after queue drained = %d, want 0",got) }
	if got:=limiter.QueueDepth();got!=0 { t.Fatalf("queue depth after queue drained = %d, want 0",got) }
}

func TestConcurrencyLimiterQueueCancellationRemovesWaiter(t *testing.T) {
	limiter,err:=NewConcurrencyLimiter(1)
	if err!=nil { t.Fatal(err) }
	if err:=limiter.SetQueueLimit(1);err!=nil { t.Fatal(err) }
	if !limiter.TryAcquire() { t.Fatal("expected initial active request to acquire the slot") }
	ctx,cancel:=context.WithCancel(context.Background())
	done:=make(chan error,1)
	go func(){ done<-limiter.Acquire(ctx,2*time.Second) }()
	waitForConcurrencyQueueDepth(t,limiter,1)
	cancel()
	select {
	case err:=<-done:
		if !errors.Is(err,context.Canceled) { t.Fatalf("cancelled waiter error = %v, want context.Canceled",err) }
	case <-time.After(2*time.Second): t.Fatal("cancelled waiter did not leave the queue")
	}
	if got:=limiter.QueueDepth();got!=0 { t.Fatalf("queue depth after cancellation = %d, want 0",got) }
	limiter.Release()
	if err:=limiter.Acquire(context.Background(),time.Second);err!=nil { t.Fatalf("new request could not acquire released slot: %v",err) }
	limiter.Release()
}

func TestConcurrencyLimiterQueueRejectsFullAndTimesOut(t *testing.T) {
	limiter,err:=NewConcurrencyLimiter(1)
	if err!=nil { t.Fatal(err) }
	if err:=limiter.SetQueueLimit(1);err!=nil { t.Fatal(err) }
	if !limiter.TryAcquire() { t.Fatal("expected initial active request to acquire the slot") }
	waitDone:=make(chan error,1)
	go func(){ waitDone<-limiter.Acquire(context.Background(),250*time.Millisecond) }()
	waitForConcurrencyQueueDepth(t,limiter,1)
	err=limiter.Acquire(context.Background(),time.Second)
	status,code,_:=core.Unpack(err)
	if status!=503||code!="provider_queue_full" { t.Fatalf("full queue error = %d/%s, want 503/provider_queue_full",status,code) }
	select {
	case err:=<-waitDone:
		status,code,_:=core.Unpack(err)
		if status!=503||code!="provider_queue_timeout" { t.Fatalf("queue timeout error = %d/%s, want 503/provider_queue_timeout",status,code) }
	case <-time.After(2*time.Second): t.Fatal("queued request did not time out")
	}
	if got:=limiter.QueueTimeouts();got!=1 { t.Fatalf("queue timeout counter = %d, want 1",got) }
	if got:=limiter.QueueDepth();got!=0 { t.Fatalf("queue depth after timeout = %d, want 0",got) }
	limiter.Release()
}


func TestConcurrencyLimiterQueueRejectsGrantAfterDeadline(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.SetQueueLimit(1); err != nil {
		t.Fatal(err)
	}
	if !limiter.TryAcquire() {
		t.Fatal("expected initial active request to acquire the slot")
	}

	done := make(chan error, 1)
	go func() {
		done <- limiter.Acquire(context.Background(), 60*time.Millisecond)
	}()
	waitForConcurrencyQueueDepth(t, limiter, 1)

	// Hold the limiter lock past the wait deadline. A release then grants the
	// queued waiter after its deadline, while its goroutine cannot observe the
	// timer until the lock is released. This makes the timeout/grant race deterministic.
	limiter.mu.Lock()
	time.Sleep(100 * time.Millisecond)
	if limiter.active <= 0 {
		limiter.mu.Unlock()
		t.Fatal("expected an active request to release")
	}
	limiter.active--
	limiter.dispatchWaitersLocked()
	limiter.mu.Unlock()

	select {
	case err := <-done:
		status, code, _ := core.Unpack(err)
		if status != 503 || code != "provider_queue_timeout" {
			t.Fatalf("late grant result = %d/%s, want 503/provider_queue_timeout", status, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late-granted request did not return")
	}
	if got := limiter.Active(); got != 0 {
		t.Fatalf("active slots after late-grant timeout = %d, want 0", got)
	}
	if got := limiter.QueueDepth(); got != 0 {
		t.Fatalf("queue depth after late-grant timeout = %d, want 0", got)
	}
	if got := limiter.QueueTimeouts(); got != 1 {
		t.Fatalf("queue timeout counter = %d, want 1", got)
	}
}
