package server

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
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
	if reasons := benchmarkComparisonReasons(left, right); !containsBenchmarkComparisonReason(reasons, "scenario_mismatch") {
		t.Fatalf("different scenarios must not compare, got %v", reasons)
	}
	right = left
	right.ConditionStatus = "partial"
	if reasons := benchmarkComparisonReasons(left, right); !containsBenchmarkComparisonReason(reasons, "condition_incomplete") {
		t.Fatalf("partial conditions must not compare, got %v", reasons)
	}
	right = left
	right.ConditionSHA256 = "different"
	if reasons := benchmarkComparisonReasons(left, right); !containsBenchmarkComparisonReason(reasons, "condition_mismatch") {
		t.Fatalf("different condition fingerprints must not compare, got %v", reasons)
	}
	right = left
	right.Status = "failed"
	if reasons := benchmarkComparisonReasons(left, right); !containsBenchmarkComparisonReason(reasons, "task_not_completed") {
		t.Fatalf("failed tasks must not compare, got %v", reasons)
	}
}

func containsBenchmarkComparisonReason(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}


func TestBenchmarkConditionSnapshotFromConfigJSON(t *testing.T) {
	raw := `{"backend":"llamacpp","condition_status":"partial","environment":{"vcs_revision":"abc123"},"model_identity":{"sha256":"deadbeef"},"runtime_config":{"warm_start":true}}`
	got := benchmarkConditionSnapshotFromConfigJSON(raw)
	if got == nil { t.Fatal("saved condition snapshot should be restored") }
	environment, ok := got["environment"].(map[string]interface{})
	if !ok || environment["vcs_revision"] != "abc123" { t.Fatalf("environment evidence lost: %#v", got["environment"]) }
	identity, ok := got["model_identity"].(map[string]interface{})
	if !ok || identity["sha256"] != "deadbeef" { t.Fatalf("model identity evidence lost: %#v", got["model_identity"]) }
	if _, ok := got["runtime_config"].(map[string]interface{}); !ok { t.Fatalf("runtime config evidence lost: %#v", got["runtime_config"]) }
}

func TestBenchmarkConditionSnapshotFromConfigJSONDoesNotInventLegacySnapshot(t *testing.T) {
	for _, raw := range []string{"", "{}", "not-json"} {
		if got := benchmarkConditionSnapshotFromConfigJSON(raw); got != nil { t.Errorf("snapshot for %q = %#v, want nil", raw, got) }
	}
}


func TestBenchmarkModelLoadStateUsesFreshMatchingRuntimeEvidence(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	fresh := &runtimeSnapshotSample{
		Status: "complete", SampledAt: now.Add(-5 * time.Second),
		Snapshot: &llama.RuntimeSnapshot{
			Backend: "llamacpp",
			Loaded: []llama.CatalogItem{{ID: "Qwen3.5-4B-IQ4_NL", Loaded: true}},
			Catalog: []llama.CatalogItem{{ID: "other-model", Status: "unloaded"}},
		},
	}
	if got := benchmarkModelLoadState(fresh, "llama.cpp", "Qwen3.5-4B-IQ4_NL", now); got != "loaded" { t.Fatalf("loaded state = %q", got) }
	fresh.Snapshot.Loaded = nil
	fresh.Snapshot.Catalog = []llama.CatalogItem{{ID: "Qwen3.5-4B-IQ4_NL", Status: "unloaded"}}
	if got := benchmarkModelLoadState(fresh, "llamacpp", "Qwen3.5-4B-IQ4_NL", now); got != "not_loaded" { t.Fatalf("unloaded state = %q", got) }
	if got := benchmarkModelLoadState(fresh, "ollama", "Qwen3.5-4B-IQ4_NL", now); got != "unknown" { t.Fatalf("mismatched backend state = %q", got) }
	fresh.SampledAt = now.Add(-runtimeSnapshotMaxAge - time.Second)
	if got := benchmarkModelLoadState(fresh, "llamacpp", "Qwen3.5-4B-IQ4_NL", now); got != "unknown" { t.Fatalf("stale state = %q", got) }
}

func TestClassifyBenchmarkAnalysis(t *testing.T) {
	valid := encodeRequestAnalysis(buildRequestAnalysis(analysisInput{TotalMS: 123}))
	tests := []struct {
		name string
		raw string
		err error
		want string
	}{
		{name: "persisted", raw: valid, want: "persisted"},
		{name: "not recorded", want: "not_recorded"},
		{name: "request log missing", err: sql.ErrNoRows, want: "request_log_missing"},
		{name: "read failed", err: errors.New("database unavailable"), want: "read_failed"},
		{name: "invalid json", raw: "{", want: "invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, note, analysis := classifyBenchmarkAnalysis(tc.raw, tc.err)
			if status != tc.want {
				t.Fatalf("status = %q, want %q (note=%q)", status, tc.want, note)
			}
			if tc.want == "persisted" && analysis == nil {
				t.Fatal("persisted analysis must be returned")
			}
			if tc.want != "persisted" && analysis != nil {
				t.Fatalf("unexpected analysis for status %q: %+v", status, analysis)
			}
			if tc.want != "persisted" && note == "" {
				t.Fatalf("status %q must explain analysis availability", status)
			}
		})
	}
}
