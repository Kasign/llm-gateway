package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeBenchmarkRequestBodyAcceptsSingleJSONValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"prompt":"hello"}`))
	w := httptest.NewRecorder()
	var got struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeBenchmarkRequestBody(w, r, &got, 1024); err != nil {
		t.Fatalf("decodeBenchmarkRequestBody() error = %v", err)
	}
	if got.Prompt != "hello" {
		t.Fatalf("decoded prompt = %q, want hello", got.Prompt)
	}
}

func TestDecodeBenchmarkRequestBodyRejectsTrailingJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"prompt":"hello"} {}`))
	w := httptest.NewRecorder()
	var got map[string]any
	if err := decodeBenchmarkRequestBody(w, r, &got, 1024); err == nil {
		t.Fatal("decodeBenchmarkRequestBody() must reject trailing JSON")
	}
}

func TestDecodeBenchmarkRequestBodyEnforcesLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"x":1}`))
	w := httptest.NewRecorder()
	var got map[string]any
	if err := decodeBenchmarkRequestBody(w, r, &got, 4); err == nil {
		t.Fatal("decodeBenchmarkRequestBody() must reject an oversized body")
	}
}

func TestParseBenchmarkTaskListQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/benchmark?scenario=tool_performance&status=completed&limit=50&offset=100", nil)
	got, err := parseBenchmarkTaskListQuery(r)
	if err != nil { t.Fatalf("parseBenchmarkTaskListQuery() error = %v", err) }
	if got.Scenario != "tool_performance" || got.Status != "completed" || got.Limit != 50 || got.Offset != 100 { t.Fatalf("parsed query = %+v", got) }
	for _, target := range []string{"/api/benchmark?limit=0","/api/benchmark?limit=101","/api/benchmark?offset=-1","/api/benchmark?scenario=unknown","/api/benchmark?status=unknown"} {
		if _, err := parseBenchmarkTaskListQuery(httptest.NewRequest(http.MethodGet, target, nil)); err == nil { t.Errorf("parseBenchmarkTaskListQuery(%q) must reject invalid query", target) }
	}
}

func TestBenchmarkComparisonReasonsRequireMatchingCompleteConditions(t *testing.T) {
	left := benchmarkComparisonSide{Scenario: "tool_performance", Status: "completed", ConditionStatus: "complete", ConditionSHA256: "same"}
	right := left
	if reasons := benchmarkComparisonReasons(left, right); len(reasons) != 0 {
		t.Fatalf("matching completed tasks should be comparable, got %v", reasons)
	}
	right.Scenario = "performance"
	if reasons := benchmarkComparisonReasons(left, right); !containsString(reasons, "scenario_mismatch") {
		t.Fatalf("different scenarios must not compare, got %v", reasons)
	}
	right = left
	right.ConditionStatus = "partial"
	if reasons := benchmarkComparisonReasons(left, right); !containsString(reasons, "condition_incomplete") {
		t.Fatalf("partial conditions must not compare, got %v", reasons)
	}
	right = left
	right.ConditionSHA256 = "different"
	if reasons := benchmarkComparisonReasons(left, right); !containsString(reasons, "condition_mismatch") {
		t.Fatalf("different condition fingerprints must not compare, got %v", reasons)
	}
	right = left
	right.Status = "failed"
	if reasons := benchmarkComparisonReasons(left, right); !containsString(reasons, "task_not_completed") {
		t.Fatalf("failed tasks must not compare, got %v", reasons)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
