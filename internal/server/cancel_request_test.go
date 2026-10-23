package server

import (
	"context"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/store"
)

func TestStopRunUsesCallWhenRunsMisses(t *testing.T) {
	s := &Server{runs: newRunCancels()}
	ctx, cancel := context.WithCancel(context.Background())
	s.callBegin("req_1", nil)
	s.setCallCancel("req_1", cancel)
	if !s.stopRun("req_1") {
		t.Fatal("expected stop")
	}
	if ctx.Err() == nil {
		t.Fatal("context still active")
	}
	if got := s.cancelReason("req_1"); got != "管理端结束这次调用" {
		t.Fatalf("reason %q", got)
	}
}

func TestCloseRunningRequestDropsLive(t *testing.T) {
	s := &Server{live: live.NewRegistry()}
	s.live.Register(store.RequestLog{RequestID: "req_live", Status: "running", StartedAt: time.Now().Add(-time.Second)})
	if !s.closeRunningRequest(context.Background(), "req_live") {
		t.Fatal("expected live request to close")
	}
	if s.liveSession("req_live") != nil {
		t.Fatal("live session still present")
	}
	if s.closeRunningRequest(context.Background(), "req_live") {
		t.Fatal("already closed request should not count")
	}
}

func TestFinishStatusKeepsSpecificReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, state, code, msg := finishStatus(ctx, core.Event{Err: context.Canceled}, "写回客户端失败：closed")
	if state != "cancelled" || code != "cancelled" || msg != "写回客户端失败：closed" {
		t.Fatalf("%s %s %s", state, code, msg)
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer timeoutCancel()
	time.Sleep(time.Millisecond)
	_, _, state, code, msg = finishStatus(timeoutCtx, core.Event{Err: context.DeadlineExceeded}, "")
	if state != "error" || code != "gateway_timeout" || msg != "网关请求超时" {
		t.Fatalf("%s %s %s", state, code, msg)
	}
}

func TestCancelReasonKeepsTheFirst(t *testing.T) {
	s := &Server{}
	s.callBegin("req_reason", nil)
	s.noteCancelReason("req_reason", "保活写出失败：closed")
	s.noteCancelReason("req_reason", "管理端结束这次调用")
	if got := s.cancelReason("req_reason"); got != "保活写出失败：closed" {
		t.Fatalf("reason %q", got)
	}
}

func TestNormalizeRequestID(t *testing.T) {
	if got := normalizeRequestID(" req_1/cancel "); got != "req_1/cancel" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeRequestID("req_1%2Fextra"); got != "req_1/extra" {
		t.Fatalf("got %q", got)
	}
}


func TestStopRunRejectsCancellationAfterFinishStarted(t *testing.T) {
	s := &Server{runs: newRunCancels()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const id = "req_finishing"
	s.callBegin(id, nil)
	s.setCallCancel(id, cancel)
	s.runs.Register(id, cancel)
	s.markCallFinishing(id)
	s.runs.MarkFinishing(id)

	if s.stopRun(id) {
		t.Fatal("stopRun must reject cancellation after Finish started")
	}
	if ctx.Err() != nil {
		t.Fatal("late cancellation changed the request context")
	}
}

func TestStopRunAcceptedBeforeFinishIsPreserved(t *testing.T) {
	s := &Server{runs: newRunCancels()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const id = "req_cancel_first"
	s.callBegin(id, nil)
	s.setCallCancel(id, cancel)
	s.runs.Register(id, cancel)

	if !s.stopRun(id) {
		t.Fatal("expected active request to be cancelled")
	}
	if !s.markCallFinishing(id) {
		t.Fatal("Finish did not observe the accepted cancellation")
	}
	if !s.runs.MarkFinishing(id) {
		t.Fatal("run registry did not preserve the accepted cancellation")
	}
	if ctx.Err() == nil {
		t.Fatal("request context was not cancelled")
	}
	if got := s.cancelReason(id); got != "管理端结束这次调用" {
		t.Fatalf("cancel reason = %q", got)
	}
}

func TestRunCancelsRejectsCancelAfterFinishStarted(t *testing.T) {
	runs := newRunCancels()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs.Register("req_run_finish", cancel)
	if runs.MarkFinishing("req_run_finish") {
		t.Fatal("unexpected cancellation request before Finish")
	}
	if runs.Cancel("req_run_finish") {
		t.Fatal("Cancel accepted after Finish started")
	}
	if ctx.Err() != nil {
		t.Fatal("context was cancelled after Finish started")
	}
}

func TestRunCancelsFinishObservesEarlierCancellation(t *testing.T) {
	runs := newRunCancels()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs.Register("req_run_cancel", cancel)
	if !runs.Cancel("req_run_cancel") {
		t.Fatal("active run cancellation was not accepted")
	}
	if !runs.MarkFinishing("req_run_cancel") {
		t.Fatal("Finish did not observe the earlier accepted cancellation")
	}
	if ctx.Err() == nil {
		t.Fatal("context was not cancelled")
	}
}
