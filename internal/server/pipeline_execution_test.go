package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/auth"
)

func TestBeginInferenceRunSharesRunLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name               string
		registerCallCancel bool
	}{
		{name: "standard_http_request", registerCallCancel: true},
		{name: "benchmark_worker", registerCallCancel: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const requestID = "shared-run-lifecycle"
			s := &Server{runs: newRunCancels()}
			key := &auth.Key{ID: 17, Name: "test-key"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cleanup := s.beginInferenceRun(requestID, key, cancel, tc.registerCallCancel)
			cs := s.callGet(requestID)
			if cs == nil {
				t.Fatal("call state must exist while Pipeline.Run is active")
			}
			if cs.key != key {
				t.Fatal("call state must retain the authenticated client key")
			}
			cs.mu.Lock()
			hasCallCancel := cs.cancel != nil
			cs.mu.Unlock()
			if hasCallCancel != tc.registerCallCancel {
				t.Fatalf("call cancel registration = %v, want %v", hasCallCancel, tc.registerCallCancel)
			}
			if !s.runs.Cancel(requestID) {
				t.Fatal("active run must be cancellable")
			}
			if ctx.Err() != context.Canceled {
				t.Fatalf("run context error = %v, want context.Canceled", ctx.Err())
			}
			cleanup()
			if s.callGet(requestID) != nil {
				t.Fatal("call state must be removed after Pipeline.Run")
			}
			if s.runs.Cancel(requestID) {
				t.Fatal("run cancellation registration must be removed after Pipeline.Run")
			}
		})
	}
}
