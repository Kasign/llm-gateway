package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/provider"
)

func TestProviderListIncludesCurrentProcessConcurrencyUsage(t *testing.T) {
	limiter, err := provider.NewConcurrencyLimiter(3)
	if err != nil {
		t.Fatalf("NewConcurrencyLimiter: %v", err)
	}
	if !limiter.TryAcquire() {
		t.Fatal("expected to acquire a concurrency slot")
	}
	if err := limiter.SetQueueLimit(2); err != nil {
		t.Fatalf("SetQueueLimit: %v", err)
	}

	s := &Server{
		cfg: &config.Config{},
		webProviders: map[string]managedProvider{
			"sample": {
				ID: "sample", Enabled: true, BaseURL: "https://provider.example/v1",
				DefaultModel: "sample-model", MaxConcurrentRequests: 3, MaxQueueSize: 2,
				ConcurrencyLimiter: limiter,
			},
		},
	}
	views := s.listManagedProviderViewsWithRPMUsage(context.Background())
	if len(views) != 1 {
		t.Fatalf("provider view count = %d, want 1", len(views))
	}
	view := views[0]
	for key, want := range map[string]int{
		"concurrent_requests_used": 1,
		"concurrent_requests_limit": 3,
		"concurrent_requests_remaining": 2,
		"queue_limit": 2,
		"queued_requests": 0,
	} {
		got, ok := view[key].(int)
		if !ok || got != want {
			t.Fatalf("%s = %#v, want %d", key, view[key], want)
		}
	}
	if got, _ := view["queue_timeouts"].(int64); got != 0 {
		t.Fatalf("queue_timeouts = %d, want 0", got)
	}
	if got, _ := view["concurrent_requests_usage_available"].(bool); !got {
		t.Fatal("concurrency usage should be available for a configured limiter")
	}
}


func TestPrepareProviderQueueLimitRespectsRegistryTransition(t *testing.T) {
	limiter, err := provider.NewConcurrencyLimiter(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := limiter.SetQueueLimit(8); err != nil {
		t.Fatal(err)
	}
	previous, changed, pending, err := prepareProviderQueueLimit("save", true, true, limiter, 3)
	if err != nil || !changed || pending || previous != 8 || limiter.QueueLimit() != 3 {
		t.Fatalf("queue reduction = previous:%d changed:%v pending:%v limit:%d err:%v", previous, changed, pending, limiter.QueueLimit(), err)
	}
	previous, changed, pending, err = prepareProviderQueueLimit("save", true, true, limiter, 6)
	if err != nil || changed || !pending || previous != 3 || limiter.QueueLimit() != 3 {
		t.Fatalf("queue increase must wait for Registry: previous:%d changed:%v pending:%v limit:%d err:%v", previous, changed, pending, limiter.QueueLimit(), err)
	}
	if err := limiter.SetQueueLimit(6); err != nil {
		t.Fatal(err)
	}
	previous, changed, pending, err = prepareProviderQueueLimit("save", true, false, limiter, 1)
	if err != nil || changed || pending || previous != 6 || limiter.QueueLimit() != 6 {
		t.Fatalf("editing disabled Provider must not change runtime queue: previous:%d changed:%v pending:%v limit:%d err:%v", previous, changed, pending, limiter.QueueLimit(), err)
	}
}
