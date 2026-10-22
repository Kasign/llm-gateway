package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newRedisTestLimiter(t *testing.T) *Limiter {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; integration test requires Redis")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &Limiter{r: client}
}

func TestAllowRepairsMissingTTLAtomically(t *testing.T) {
	limiter := newRedisTestLimiter(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)
	key := "ttl-repair-" + t.Name()
	redisKey := rateLimitBucketKey(key, now)
	if err := limiter.r.Set(ctx, redisKey, 7, 0).Err(); err != nil {
		t.Fatal(err)
	}

	allowed, count, err := limiter.allowAt(ctx, key, 10, now)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if !allowed || count != 8 {
		t.Fatalf("allowed/count = %v/%d, want true/8", allowed, count)
	}
	ttl, err := limiter.r.TTL(ctx, redisKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > 70*time.Second {
		t.Fatalf("TTL = %s, want (0, 70s]", ttl)
	}
}

func TestAllowHonorsRPMWithinMinuteBucket(t *testing.T) {
	limiter := newRedisTestLimiter(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)
	key := "rpm-boundary-" + t.Name()

	for i, wantAllowed := range []bool{true, true, false} {
		allowed, count, err := limiter.allowAt(ctx, key, 2, now)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if allowed != wantAllowed || count != int64(i+1) {
			t.Fatalf("request %d: allowed/count=%v/%d, want %v/%d", i+1, allowed, count, wantAllowed, i+1)
		}
	}
}
