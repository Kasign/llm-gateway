package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/store"
)

func acceptanceEvidence(caseID string) (benchmarkJobSnapshot, benchmarkToolMetrics) {
	item, ok := benchmarkAcceptanceCaseByID(caseID)
	if !ok {
		panic("unknown test case")
	}
	job := benchmarkJobSnapshot{
		Scenario: item.Scenario,
		Status: "completed",
		HTTPStatus: 200,
		ToolCallDecision: "called",
		ToolCallEventCount: 1,
		ModelRoundDurationsMS: []int64{100, 120},
	}
	metrics := benchmarkToolMetrics{
		ToolChoiceMode: item.ToolChoiceMode,
		ToolCallDecision: "called",
		ToolExecutionStatus: "measured",
		ToolExecutionAttempts: 1,
		ToolExecutionSuccesses: 1,
		ModelRounds: 2,
		ModelFinalGenerationStatus: "measured",
	}
	return job, metrics
}

func TestBenchmarkAcceptanceCatalog(t *testing.T) {
	catalog := benchmarkAcceptanceCatalog()
	if len(catalog) != 5 {
		t.Fatalf("catalog size = %d, want 5", len(catalog))
	}
	for _, id := range []string{"TM-01", "TM-02", "TM-03", "TM-04", "TM-05"} {
		if _, ok := benchmarkAcceptanceCaseByID(id); !ok {
			t.Errorf("case %s is missing", id)
		}
	}
	tm03, _ := benchmarkAcceptanceCaseByID("tm-03")
	if !tm03.ManualOnly {
		t.Fatal("TM-03 must be marked as manual/isolated-environment only")
	}
}

func TestEvaluateBenchmarkAcceptancePassesExpectedEvidence(t *testing.T) {
	tests := []struct {
		id string
		prepare func(*benchmarkJobSnapshot, *benchmarkToolMetrics)
	}{
		{ id: "TM-01" },
		{ id: "TM-02", prepare: func(job *benchmarkJobSnapshot, metrics *benchmarkToolMetrics) {
			job.ToolCallDecision = "not_called"
			job.ToolCallEventCount = 0
			metrics.ToolCallDecision = "not_called"
			metrics.ToolExecutionStatus = "not_applicable"
			metrics.ToolExecutionAttempts = 0
			metrics.ToolExecutionSuccesses = 0
			metrics.ModelRounds = 1
			metrics.ModelFinalGenerationStatus = "combined_with_decision"
		}},
		{ id: "TM-03", prepare: func(job *benchmarkJobSnapshot, metrics *benchmarkToolMetrics) {
			job.Status = "failed"
			metrics.ToolExecutionFailures = 1
			metrics.ToolExecutionSuccesses = 0
		}},
		{ id: "TM-04" },
		{ id: "TM-05" },
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			job, metrics := acceptanceEvidence(tt.id)
			if tt.prepare != nil {
				tt.prepare(&job, &metrics)
			}
			got := evaluateBenchmarkAcceptanceWithEvents(tt.id, job, metrics, acceptanceTestEvents(tt.id))
			if got.Status != "pass" {
				t.Fatalf("result = %+v, want pass", got)
			}
			if got.RulesVersion != benchmarkAcceptanceRulesVersion {
				t.Fatalf("rules version = %q", got.RulesVersion)
			}
		})
	}
}

func TestEvaluateBenchmarkAcceptanceDistinguishesFailureFromInsufficientEvidence(t *testing.T) {
	t.Run("unknown decision is inconclusive", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-01")
		job.ToolCallDecision = "unknown"
		metrics.ToolCallDecision = "unknown"
		metrics.ToolExecutionStatus = "unknown"
		got := evaluateBenchmarkAcceptance("TM-01", job, metrics)
		if got.Status != "inconclusive" || got.ReasonCode != "evidence_insufficient" {
			t.Fatalf("result = %+v, want inconclusive evidence_insufficient", got)
		}
	})
	t.Run("explicit missing call fails", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-01")
		job.ToolCallDecision = "not_called"
		metrics.ToolCallDecision = "not_called"
		got := evaluateBenchmarkAcceptance("TM-01", job, metrics)
		if got.Status != "fail" || got.ReasonCode != "expected_tool_call_missing" {
			t.Fatalf("result = %+v, want expected_tool_call_missing", got)
		}
	})
	t.Run("audit incomplete is inconclusive", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-04")
		metrics.ToolExecutionStatus = "unknown"
		metrics.ToolExecutionSuccesses = 0
		got := evaluateBenchmarkAcceptance("TM-04", job, metrics)
		if got.Status != "inconclusive" {
			t.Fatalf("result = %+v, want inconclusive", got)
		}
	})
	t.Run("task still running is not_run", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-01")
		job.Status = "running"
		got := evaluateBenchmarkAcceptance("TM-01", job, metrics)
		if got.Status != "not_run" || got.ReasonCode != "task_not_finished" {
			t.Fatalf("result = %+v, want not_run", got)
		}
	})
	t.Run("wrong scenario fails", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-05")
		job.Scenario = "tool_performance_auto"
		got := evaluateBenchmarkAcceptance("TM-05", job, metrics)
		if got.Status != "fail" || got.ReasonCode != "scenario_mismatch" {
			t.Fatalf("result = %+v, want scenario_mismatch", got)
		}
	})
	t.Run("unknown case is rejected", func(t *testing.T) {
		job, metrics := acceptanceEvidence("TM-01")
		got := evaluateBenchmarkAcceptance("TM-99", job, metrics)
		if got.Status != "inconclusive" || got.ReasonCode != "unknown_case" {
			t.Fatalf("result = %+v, want unknown_case", got)
		}
	})
}

func TestEvaluateBenchmarkAcceptanceTM04RequiresSecondModelRound(t *testing.T) {
	job, metrics := acceptanceEvidence("TM-04")
	job.ModelRoundDurationsMS = []int64{100}
	metrics.ModelRounds = 1
	metrics.ModelFinalGenerationStatus = "unknown"
	got := evaluateBenchmarkAcceptance("TM-04", job, metrics)
	if got.Status != "inconclusive" {
		t.Fatalf("result = %+v, want inconclusive when second model round is absent", got)
	}
}

func TestEvaluateBenchmarkAcceptanceTM03RequiresObservedFailure(t *testing.T) {
	job, metrics := acceptanceEvidence("TM-03")
	got := evaluateBenchmarkAcceptanceWithEvents("TM-03", job, metrics, []store.ToolCallEvent{{Status: "failed"}})
	if got.Status != "fail" || got.ReasonCode != "tool_failure_not_observed" {
		t.Fatalf("result = %+v, want tool_failure_not_observed", got)
	}
}

func TestBenchmarkAcceptanceCaseIDFromConfigJSON(t *testing.T) {
	if got := benchmarkAcceptanceCaseIDFromConfigJSON(`{"acceptance_case_id":"tm-04"}`); got != "TM-04" {
		t.Fatalf("case ID = %q, want TM-04", got)
	}
	for _, raw := range []string{"", "{", `{"scenario":"tool_performance_auto"}`} {
		if got := benchmarkAcceptanceCaseIDFromConfigJSON(raw); got != "" {
			t.Errorf("case ID from %q = %q, want empty", raw, got)
		}
	}
}

func acceptanceTestEvents(caseID string) []store.ToolCallEvent {
	if caseID == "TM-03" {
		return []store.ToolCallEvent{{Status: "failed"}}
	}
	return []store.ToolCallEvent{{Status: "succeeded"}}
}

func TestEvaluateBenchmarkAcceptanceTM03DoesNotTreatCancellationAsFailure(t *testing.T) {
	job, metrics := acceptanceEvidence("TM-03")
	metrics.ToolExecutionFailures = 1
	got := evaluateBenchmarkAcceptanceWithEvents("TM-03", job, metrics, []store.ToolCallEvent{{Status: "cancelled"}})
	if got.Status != "fail" || got.ReasonCode != "tool_failure_not_observed" {
		t.Fatalf("result = %+v, want failure not observed for cancellation only", got)
	}
}

func TestEvaluateBenchmarkAcceptanceRequiresFinalModelGeneration(t *testing.T) {
	for _, id := range []string{"TM-01", "TM-05"} {
		job, metrics := acceptanceEvidence(id)
		metrics.ModelRounds = 1
		metrics.ModelFinalGenerationStatus = "unknown"
		got := evaluateBenchmarkAcceptanceWithEvents(id, job, metrics, acceptanceTestEvents(id))
		if got.Status != "inconclusive" {
			t.Errorf("%s result = %+v, want inconclusive without final generation evidence", id, got)
		}
	}
}
