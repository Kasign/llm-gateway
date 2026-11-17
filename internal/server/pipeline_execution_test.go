package server

import (
	"context"
	"errors"
	"testing"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
)

func TestRunInferencePipelineSharesRunLifecycle(t *testing.T) {
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

			s.pipe = &core.Pipeline{
				Resolve: func(runCtx context.Context, st core.State) (core.ModelChoice, error) {
					cs := s.callGet(st.ID)
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
					if !s.runs.Cancel(st.ID) {
						t.Fatal("active run must be cancellable")
					}
					if !errors.Is(runCtx.Err(), context.Canceled) {
						t.Fatalf("run context error = %v, want context.Canceled", runCtx.Err())
					}
					return core.ModelChoice{}, runCtx.Err()
				},
			}

			_, err := s.runInferencePipeline(ctx, core.Request{
				ID: requestID, Method: "POST", Path: "/v1/chat/completions",
			}, key, cancel, tc.registerCallCancel)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runInferencePipeline() error = %v, want context.Canceled", err)
			}
			if s.callGet(requestID) != nil {
				t.Fatal("call state must be removed after Pipeline.Run")
			}
			if s.runs.Cancel(requestID) {
				t.Fatal("run cancellation registration must be removed after Pipeline.Run")
			}
		})
	}
}
