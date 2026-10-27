package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
)

func TestNewInferencePipelineAppliesDeploymentProviderQueueOptions(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "queued-remote", Enabled: true,
		BaseURL: "https://provider.example/v1", DefaultModel: "chat-model",
		MaxConcurrentRequests: 1, MaxQueueSize: 2, QueueWaitTimeoutSeconds: 1,
	}}
	s := &Server{cfg: cfg}
	s.newInferencePipeline()

	registered, err := s.providers.Resolve("queued-remote")
	if err != nil {
		t.Fatalf("deployment Provider was not registered: %v", err)
	}
	remote, ok := registered.(*provider.OpenAICompatibleProvider)
	if !ok {
		t.Fatalf("registered Provider type = %T, want *OpenAICompatibleProvider", registered)
	}
	limiter := remote.ConcurrencyLimiter()
	if limiter == nil {
		t.Fatal("deployment Provider has no concurrency limiter")
	}
	if got := limiter.QueueLimit(); got != 2 {
		t.Fatalf("deployment queue limit = %d, want 2", got)
	}
	if !limiter.TryAcquire() {
		t.Fatal("failed to hold the only concurrency slot for the queue timeout check")
	}
	defer limiter.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	writer := httptest.NewRecorder()
	result, forwardErr := remote.Forward(ctx, provider.InferenceCall{
		Writer: writer,
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		Path: "/v1/chat/completions",
		Body: []byte(`{"model":"public-model"}`),
		ModelID: "public-model",
	})
	if forwardErr == nil {
		t.Fatal("request should time out while waiting for the occupied Provider slot")
	}
	status, code, _ := core.Unpack(forwardErr)
	if status != http.StatusServiceUnavailable || code != "provider_queue_timeout" {
		t.Fatalf("queue wait error = %d/%s, want 503/provider_queue_timeout (result=%+v)", status, code, result)
	}
	if result.Status != http.StatusServiceUnavailable {
		t.Fatalf("result status = %d, want 503", result.Status)
	}
}
