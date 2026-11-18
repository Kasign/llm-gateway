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

func TestConcurrencyLimiterQueueObserverRecordsTimeout(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil { t.Fatal(err) }
	if err := limiter.SetQueueLimit(1); err != nil { t.Fatal(err) }
	if !limiter.TryAcquire() { t.Fatal("expected initial active request to acquire the slot") }

	observedCh := make(chan ConcurrencyWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- limiter.AcquireWithObserver(context.Background(), 60*time.Millisecond, func(v ConcurrencyWaitResult) {
			observedCh <- v
		})
	}()
	waitForConcurrencyQueueDepth(t, limiter, 1)

	select {
	case err := <-errCh:
		status, code, _ := core.Unpack(err)
		if status != 503 || code != "provider_queue_timeout" {
			t.Fatalf("timeout error = %d/%s, want 503/provider_queue_timeout", status, code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued request did not time out")
	}
	select {
	case observed := <-observedCh:
		if !observed.Waited || observed.Status != "timeout" || observed.WaitMS < 40 {
			t.Fatalf("timeout observation = %+v, want waited timeout >= 40ms", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout observer did not report")
	}
	limiter.Release()
}

func TestConcurrencyLimiterQueueObserverRecordsCancellation(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil { t.Fatal(err) }
	if err := limiter.SetQueueLimit(1); err != nil { t.Fatal(err) }
	if !limiter.TryAcquire() { t.Fatal("expected initial active request to acquire the slot") }

	ctx, cancel := context.WithCancel(context.Background())
	observedCh := make(chan ConcurrencyWaitResult, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- limiter.AcquireWithObserver(ctx, 2*time.Second, func(v ConcurrencyWaitResult) {
			observedCh <- v
		})
	}()
	waitForConcurrencyQueueDepth(t, limiter, 1)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled waiter did not return")
	}
	select {
	case observed := <-observedCh:
		if !observed.Waited || observed.Status != "cancelled" || observed.WaitMS < 0 {
			t.Fatalf("cancellation observation = %+v, want waited cancelled", observed)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation observer did not report")
	}
	if got := limiter.QueueDepth(); got != 0 {
		t.Fatalf("queue depth after cancellation = %d, want 0", got)
	}
	limiter.Release()
}


func TestConcurrencyLimiterQueueObserverRecordsDirectAndGrantedWait(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil { t.Fatal(err) }
	if err := limiter.SetQueueLimit(1); err != nil { t.Fatal(err) }

	var direct ConcurrencyWaitResult
	if err := limiter.AcquireWithObserver(context.Background(), time.Second, func(v ConcurrencyWaitResult) { direct = v }); err != nil { t.Fatal(err) }
	if direct.Waited || direct.WaitMS != 0 || direct.Status != "none" {
		t.Fatalf("direct admission = %+v, want zero wait/none", direct)
	}

	waitDone := make(chan ConcurrencyWaitResult, 1)
	release := make(chan struct{})
	go func() {
		var observed ConcurrencyWaitResult
		err := limiter.AcquireWithObserver(context.Background(), 2*time.Second, func(v ConcurrencyWaitResult) { observed = v })
		if err == nil { waitDone <- observed; <-release; limiter.Release(); return }
		waitDone <- observed
	}()
	waitForConcurrencyQueueDepth(t, limiter, 1)
	limiter.Release()
	select {
	case observed := <-waitDone:
		if !observed.Waited || observed.Status != "granted" || observed.WaitMS < 0 {
			t.Fatalf("queued admission = %+v", observed)
		}
	case <-time.After(2*time.Second):
		t.Fatal("queued observer did not return")
	}
	close(release)
}
