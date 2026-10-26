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

	s := &Server{
		cfg: &config.Config{},
		webProviders: map[string]managedProvider{
			"sample": {
				ID: "sample", Enabled: true, BaseURL: "https://provider.example/v1",
				DefaultModel: "sample-model", MaxConcurrentRequests: 3,
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
	} {
		got, ok := view[key].(int)
		if !ok || got != want {
			t.Fatalf("%s = %#v, want %d", key, view[key], want)
		}
	}
	if got, _ := view["concurrent_requests_usage_available"].(bool); !got {
		t.Fatal("concurrency usage should be available for a configured limiter")
	}
}
