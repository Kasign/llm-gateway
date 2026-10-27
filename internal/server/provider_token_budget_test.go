package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/ratelimit"
)

func TestRequestOutputTokenLimitDefaultsByProtocol(t *testing.T) {
	object, limit, err := requestOutputTokenLimit([]byte("{\"model\":\"vendor-model\",\"messages\":[]}"))
	if err != nil || limit != defaultProviderBudgetOutputTokens {
		t.Fatalf("default output token limit=%d err=%v", limit, err)
	}
	body, err := writeRequestOutputTokenLimit(object, "/v1/responses", limit)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["max_output_tokens"]; !ok {
		t.Fatalf("Responses body lacks max_output_tokens: %s", body)
	}
	if _, ok := result["max_tokens"]; ok {
		t.Fatalf("Responses body unexpectedly has max_tokens: %s", body)
	}
}

func TestRequestOutputTokenLimitUsesStrictestFieldAndCapsEveryExistingField(t *testing.T) {
	object, limit, err := requestOutputTokenLimit([]byte("{\"max_tokens\":4096,\"max_completion_tokens\":2048}"))
	if err != nil || limit != 2048 {
		t.Fatalf("output limit=%d err=%v, want 2048", limit, err)
	}
	capped, err := writeRequestOutputTokenLimit(object, "/v1/chat/completions", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(capped, []byte("\"max_tokens\":1024")) || !bytes.Contains(capped, []byte("\"max_completion_tokens\":1024")) {
		t.Fatalf("not all output limits were capped: %s", capped)
	}
	if bytes.Contains(capped, []byte("4096")) || bytes.Contains(capped, []byte("2048")) {
		t.Fatalf("larger caller output limits remain: %s", capped)
	}
}

func TestRequestOutputTokenLimitRejectsMalformedJSONOrNonPositiveFields(t *testing.T) {
	for _, body := range [][]byte{[]byte("not-json"), []byte("{\"max_tokens\":0}"), []byte("{\"max_output_tokens\":\"1024\"}")} {
		if _, _, err := requestOutputTokenLimit(body); err == nil {
			t.Fatalf("expected invalid output limit for %s", body)
		}
	}
}

func TestProviderBudgetOutputLimitScope(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/messages"} {
		if !providerBudgetHasOutputLimit(path) {
			t.Errorf("expected output-limit support for %s", path)
		}
	}
	for _, path := range []string{"/v1/embeddings", "/v1/messages/count_tokens", "/v1/models"} {
		if providerBudgetHasOutputLimit(path) {
			t.Errorf("unexpected output-limit support for %s", path)
		}
	}
}


func newProviderBudgetTestLimiter(t *testing.T) *ratelimit.Limiter {
	t.Helper()
	addr := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDR"))
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; integration test requires Redis")
	}
	limiter := ratelimit.New(addr, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := limiter.Ping(ctx); err != nil {
		_ = limiter.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	t.Cleanup(func() { _ = limiter.Close() })
	return limiter
}

func TestProviderTokenBudgetRefundsReservationWhenDispatchNeverStarts(t *testing.T) {
	limiter := newProviderBudgetTestLimiter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Now().UTC()
	providerID := fmt.Sprintf("queue-refund-%d", now.UnixNano())
	server := &Server{limiter: limiter}

	reserve := func(requestID string, amount int64) {
		t.Helper()
		bucket, _, allowed, err := limiter.ReserveProviderTokens(ctx, providerID, 10000, amount, now)
		if err != nil {
			t.Fatalf("reserve %d tokens: %v", amount, err)
		}
		if !allowed {
			t.Fatalf("reservation of %d tokens unexpectedly rejected", amount)
		}
		server.storeProviderTokenBudgetReservation(requestID, providerTokenBudgetReservation{
			ProviderID: providerID, Bucket: bucket, ReservedTokens: amount,
		})
	}
	usage := func() int64 {
		t.Helper()
		values, err := limiter.ProviderTokenBudgetUsage(ctx, []string{providerID}, now)
		if err != nil {
			t.Fatalf("read Provider token budget: %v", err)
		}
		return values[providerID]
	}

	reserve("queue-timeout-before-dispatch", 127)
	if got := usage(); got != 127 {
		t.Fatalf("reserved usage = %d, want 127 before finish", got)
	}
	server.finishProviderTokenBudget("queue-timeout-before-dispatch", "", nil, nil, nil)
	if got := usage(); got != 0 {
		t.Fatalf("pre-dispatch rejection left %d reserved tokens, want full refund", got)
	}

	reserve("dispatch-with-unknown-usage", 89)
	server.markProviderTokenBudgetDispatched("dispatch-with-unknown-usage")
	server.finishProviderTokenBudget("dispatch-with-unknown-usage", "", nil, nil, nil)
	if got := usage(); got != 89 {
		t.Fatalf("dispatched request without trusted usage retained %d tokens, want 89", got)
	}
}
