package server

import (
	"bytes"
	"encoding/json"
	"testing"
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
