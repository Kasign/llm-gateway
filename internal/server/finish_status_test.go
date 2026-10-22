package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whl/llm-gateway/internal/core"
)

func TestFinishStatusSeparatesCommittedGatewayAndUpstreamStatus(t *testing.T) {
	sink := &responseSink{w: httptest.NewRecorder()}
	_, _ = sink.Write([]byte(": keepalive\n\n"))
	gatewayStatus, upstreamStatus, state, code, _ := finishStatus(context.Background(), core.Event{State: core.State{Sink: sink, Stream: true}, Result: &core.Result{Status: http.StatusBadRequest}}, "")
	if gatewayStatus != http.StatusOK || upstreamStatus != http.StatusBadRequest {
		t.Fatalf("gateway/upstream = %d/%d, want 200/400", gatewayStatus, upstreamStatus)
	}
	if state != "error" || code != "upstream_http_error" {
		t.Fatalf("state/code = %q/%q", state, code)
	}
}

func TestFinishStatusUsesUpstreamStatusBeforeGatewayCommits(t *testing.T) {
	sink := &responseSink{w: httptest.NewRecorder()}
	gatewayStatus, upstreamStatus, state, code, _ := finishStatus(context.Background(), core.Event{State: core.State{Sink: sink, Stream: true}, Result: &core.Result{Status: http.StatusBadRequest}}, "")
	if gatewayStatus != http.StatusBadRequest || upstreamStatus != http.StatusBadRequest {
		t.Fatalf("gateway/upstream = %d/%d, want 400/400", gatewayStatus, upstreamStatus)
	}
	if state != "error" || code != "upstream_http_error" {
		t.Fatalf("state/code = %q/%q", state, code)
	}
}

func TestFinishStatusCancellationKeepsCommittedGatewayStatus(t *testing.T) {
	sink := &responseSink{w: httptest.NewRecorder()}
	_, _ = sink.Write([]byte(": keepalive\n\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gatewayStatus, upstreamStatus, state, code, _ := finishStatus(ctx, core.Event{State: core.State{Sink: sink, Stream: true}, Result: &core.Result{Status: http.StatusOK}, Err: context.Canceled}, "客户端断开")
	if gatewayStatus != http.StatusOK || upstreamStatus != http.StatusOK {
		t.Fatalf("gateway/upstream = %d/%d, want 200/200", gatewayStatus, upstreamStatus)
	}
	if state != "cancelled" || code != "cancelled" {
		t.Fatalf("state/code = %q/%q", state, code)
	}
}
