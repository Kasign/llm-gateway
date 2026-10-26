package provider

import "testing"

func TestConcurrencyLimiterCountsRejectedRequests(t *testing.T) {
	limiter, err := NewConcurrencyLimiter(1)
	if err != nil {
		t.Fatalf("NewConcurrencyLimiter: %v", err)
	}
	if !limiter.TryAcquire() {
		t.Fatal("first request should acquire the only slot")
	}
	if limiter.TryAcquire() {
		t.Fatal("second request must be rejected while the slot is occupied")
	}
	if got := limiter.RejectedRequests(); got != 1 {
		t.Fatalf("rejected requests = %d, want 1", got)
	}
	limiter.Release()
	if !limiter.TryAcquire() {
		t.Fatal("request should acquire the slot after release")
	}
	if got := limiter.RejectedRequests(); got != 1 {
		t.Fatalf("rejected requests changed after successful acquire: %d", got)
	}
}