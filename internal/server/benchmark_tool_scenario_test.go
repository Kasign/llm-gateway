package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/whl/llm-gateway/internal/store"
)

func TestBenchmarkToolScenarioChoiceModes(t *testing.T) {
	tests := []struct {
		scenario string
		wantTool bool
		wantMode string
	}{
		{scenario: "performance", wantTool: false, wantMode: "not_applicable"},
		{scenario: "tool_performance_auto", wantTool: true, wantMode: "auto"},
		{scenario: "tool_performance", wantTool: true, wantMode: "required"},
	}
	for _, tt := range tests {
		if got := isBenchmarkToolScenario(tt.scenario); got != tt.wantTool {
			t.Errorf("isBenchmarkToolScenario(%q) = %v, want %v", tt.scenario, got, tt.wantTool)
		}
		if got := benchmarkToolChoiceMode(tt.scenario); got != tt.wantMode {
			t.Errorf("benchmarkToolChoiceMode(%q) = %q, want %q", tt.scenario, got, tt.wantMode)
		}
	}
}

func TestBuildBenchmarkToolChatBodyUsesConfiguredChoiceMode(t *testing.T) {
	input := benchRunInput{Prompt: "What is the latest Go release?", MaxTokens: 256}
	tests := []struct {
		name       string
		mode       string
		wantChoice string
		wantObject bool
	}{
		{name: "natural decision", mode: "auto", wantChoice: "auto"},
		{name: "forced diagnostic", mode: "required", wantObject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := buildBenchmarkToolChatBody("test-model", input, tt.mode)
			var request struct {
				Stream     bool            `json:"stream"`
				ToolChoice json.RawMessage `json:"tool_choice"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.Stream {
				t.Fatal("tool benchmark must be non-streaming")
			}
			if tt.wantObject {
				var choice map[string]interface{}
				if err := json.Unmarshal(request.ToolChoice, &choice); err != nil {
					t.Fatalf("decode forced tool_choice: %v", err)
				}
				if choice["type"] != "function" {
					t.Fatalf("forced tool_choice type = %v, want function", choice["type"])
				}
			} else {
				var choice string
				if err := json.Unmarshal(request.ToolChoice, &choice); err != nil {
					t.Fatalf("decode automatic tool_choice: %v", err)
				}
				if choice != tt.wantChoice {
					t.Fatalf("tool_choice = %q, want %q", choice, tt.wantChoice)
				}
			}
		})
	}
}

func TestBuildBenchmarkToolMetricsNaturalDecisionWithoutToolCall(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario:              "tool_performance_auto",
		Status:                "completed",
		ModelRoundDurationsMS: []int64{125},
		ToolCallDecision: "not_called",
	}
	metrics := buildBenchmarkToolMetrics(job, nil, true)
	if metrics.ToolChoiceMode != "auto" {
		t.Fatalf("tool_choice_mode = %q, want auto", metrics.ToolChoiceMode)
	}
	if metrics.ToolCallDecision != "not_called" {
		t.Fatalf("tool_call_decision = %q, want not_called", metrics.ToolCallDecision)
	}
	if metrics.ModelDecisionStatus != "measured" || metrics.ModelDecisionMS == nil || *metrics.ModelDecisionMS != 125 {
		t.Fatalf("model decision metric = %#v, want measured 125ms", metrics)
	}
	if metrics.ToolExecutionStatus != "not_applicable" {
		t.Fatalf("tool execution status = %q, want not_applicable", metrics.ToolExecutionStatus)
	}
	if metrics.ModelFinalGenerationStatus != "combined_with_decision" || metrics.ModelFinalGenerationMS == nil || *metrics.ModelFinalGenerationMS != 125 {
		t.Fatalf("final generation metric = %#v, want combined_with_decision 125ms", metrics)
	}
	if metrics.ToolCalls != 0 || metrics.ToolRounds != 0 {
		t.Fatalf("unexpected tool counts: calls=%d rounds=%d", metrics.ToolCalls, metrics.ToolRounds)
	}
}

func TestBuildBenchmarkToolMetricsIncludesActualToolExecution(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario:              "tool_performance_auto",
		Status:                "completed",
		ModelRoundDurationsMS: []int64{125, 230},
	}
	events := []store.ToolCallEvent{{Round: 1, Status: "succeeded", DurationMS: 55}}
	metrics := buildBenchmarkToolMetrics(job, events, true)
	if metrics.ToolExecutionStatus != "measured" || metrics.ToolExecutionTotalMS == nil || *metrics.ToolExecutionTotalMS != 55 {
		t.Fatalf("tool execution metric = %#v, want measured 55ms", metrics)
	}
	if metrics.ToolCallDecision != "called" || metrics.ToolExecutionAttempts != 1 || metrics.ToolExecutionSuccesses != 1 || metrics.ToolExecutionFailures != 0 {
		t.Fatalf("tool execution counts/decision = %#v, want called and 1/1/0", metrics)
	}
	if metrics.ToolExecutionSuccessRate == nil || *metrics.ToolExecutionSuccessRate != 100 {
		t.Fatalf("tool execution success rate = %v, want 100%%", metrics.ToolExecutionSuccessRate)
	}
	if metrics.ModelFinalGenerationStatus != "measured" || metrics.ModelFinalGenerationMS == nil || *metrics.ModelFinalGenerationMS != 230 {
		t.Fatalf("final generation metric = %#v, want measured 230ms", metrics)
	}
	if metrics.ToolCalls != 1 || metrics.ToolRounds != 1 {
		t.Fatalf("tool counts = calls:%d rounds:%d, want 1/1", metrics.ToolCalls, metrics.ToolRounds)
	}
}

func TestBuildBenchmarkToolMetricsForcedNoCallRemainsUnknown(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario:              "tool_performance",
		Status:                "failed",
		ModelRoundDurationsMS: []int64{125},
		ToolCallDecision: "unknown",
	}
	metrics := buildBenchmarkToolMetrics(job, nil, true)
	if metrics.ToolChoiceMode != "required" {
		t.Fatalf("tool_choice_mode = %q, want required", metrics.ToolChoiceMode)
	}
	if metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("tool execution status = %q, want unknown for failed forced invocation", metrics.ToolExecutionStatus)
	}
	if metrics.ToolCallDecision != "unknown" {
		t.Fatalf("tool call decision = %q, want unknown for failed forced invocation", metrics.ToolCallDecision)
	}
}

func TestBuildBenchmarkToolMetricsOrdinaryScenarioIsNotApplicable(t *testing.T) {
	metrics := buildBenchmarkToolMetrics(benchmarkJobSnapshot{Scenario: "performance", Status: "completed"}, nil, true)
	if metrics.ToolChoiceMode != "not_applicable" || metrics.ToolCallDecision != "not_applicable" {
		t.Fatalf("ordinary scenario tool mode/decision = %q/%q, want not_applicable", metrics.ToolChoiceMode, metrics.ToolCallDecision)
	}
	if metrics.ToolExecutionStatus != "not_applicable" || metrics.ModelFinalGenerationStatus != "not_applicable" {
		t.Fatalf("ordinary scenario tool statuses = %q/%q, want not_applicable", metrics.ToolExecutionStatus, metrics.ModelFinalGenerationStatus)
	}
}

func TestBuildBenchmarkToolMetricsDoesNotInferNoCallWithoutDecisionEvidence(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status:   "completed",
		ModelRoundDurationsMS: []int64{125},
	}
	metrics := buildBenchmarkToolMetrics(job, nil, true)
	if metrics.ToolCallDecision != "unknown" {
		t.Fatalf("tool call decision = %q, want unknown without event evidence", metrics.ToolCallDecision)
	}
	if metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("tool execution status = %q, want unknown without event evidence", metrics.ToolExecutionStatus)
	}
}

func TestBuildBenchmarkToolMetricsDoesNotInferNoCallFromMissingEvents(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario:              "tool_performance_auto",
		Status:                "completed",
		ModelRoundDurationsMS: []int64{125, 230},
		ToolCallDecision: "unknown",
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolCallDecision != "unknown" || metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("metrics without tool events but multiple model rounds = %#v, want unknown decision/execution", metrics)
	}
}


func TestBuildBenchmarkToolMetricsCalledDecisionWithoutAuditEventsKeepsExecutionUnknown(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status: "completed",
		ToolCallDecision: "called",
		ToolCallEventCount: 1,
		ToolCallEventCount: 1,
		ModelRoundDurationsMS: []int64{125, 230},
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolCallDecision != "called" {
		t.Fatalf("tool call decision = %q, want called", metrics.ToolCallDecision)
	}
	if metrics.ToolExecutionAttempts != 0 || metrics.ToolExecutionSuccessRate != nil || metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("execution metrics = %#v, want unknown without request audit events", metrics)
	}
}

func TestBuildBenchmarkToolMetricsUnknownDoesNotInferNoCallFromEmptyAvailableEvents(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status: "completed",
		ToolCallDecision: "unknown",
		ModelRoundDurationsMS: []int64{125},
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolCallDecision != "unknown" || metrics.ModelFinalGenerationStatus != "unknown" {
		t.Fatalf("metrics = %#v, want unknown decision and final generation", metrics)
	}
}


func TestRecordBenchmarkToolCallDecisionStoresRequestEvidence(t *testing.T) {
	job := &benchmarkJob{ToolCallDecision: "unknown"}
	ctx := withBenchmarkToolMetrics(context.Background(), job)

	recordBenchmarkToolCallDecision(ctx, "called", 2)
	if got := job.snapshot().ToolCallDecision; got != "called" {
		t.Fatalf("decision after called = %q, want called", got)
	}
	recordBenchmarkToolCallDecision(ctx, "not_called", 0)
	if got := job.snapshot().ToolCallDecision; got != "not_called" {
		t.Fatalf("decision after not_called = %q, want not_called", got)
	}
	recordBenchmarkToolCallDecision(ctx, "unknown", 0)
	if got := job.snapshot().ToolCallDecision; got != "unknown" {
		t.Fatalf("decision after unknown = %q, want unknown", got)
	}
}

func TestRecordBenchmarkToolCallDecisionIgnoresInvalidAndNonBenchmarkContexts(t *testing.T) {
	job := &benchmarkJob{ToolCallDecision: "unknown"}
	recordBenchmarkToolCallDecision(context.Background(), "called", 0)
	recordBenchmarkToolCallDecision(withBenchmarkToolMetrics(context.Background(), job), "invalid", 0)
	if got := job.snapshot().ToolCallDecision; got != "unknown" {
		t.Fatalf("decision = %q, want unknown", got)
	}
}

func TestBenchmarkPersistenceRecordsIncludeToolCallDecision(t *testing.T) {
	job := &benchmarkJob{
		ToolCallDecision: "called",
		taskRecord: store.BenchmarkTask{TaskID: "task"},
		runRecord: store.BenchmarkRun{RunID: "run", TaskID: "task"},
	}
	_, run := job.persistenceRecords()
	if run.ToolCallDecision != "called" || run.ToolCallEventCount != 0 {
		t.Fatalf("persisted tool call decision = %q, want called", run.ToolCallDecision)
	}
}


func TestBenchmarkToolCallDecisionRequiresSuccessfulFinalResponse(t *testing.T) {
	tests := []struct {
		name string
		modelRounds int
		responseStatus int
		eventCount int
		runErr error
		want string
	}{
		{name: "successful no-call response", modelRounds: 1, responseStatus: 200, want: "not_called"},
		{name: "upstream rejection", modelRounds: 1, responseStatus: 401, want: "unknown"},
		{name: "missing response status", modelRounds: 1, want: "unknown"},
		{name: "coordinator failure", modelRounds: 1, responseStatus: 200, runErr: context.Canceled, want: "unknown"},
		{name: "tool event despite execution failure", modelRounds: 1, responseStatus: 502, eventCount: 1, runErr: context.DeadlineExceeded, want: "called"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := benchmarkToolCallDecision(tt.modelRounds, tt.responseStatus, tt.eventCount, tt.runErr); got != tt.want {
				t.Fatalf("benchmarkToolCallDecision() = %q, want %q", got, tt.want)
			}
		})
	}
}


func TestBuildBenchmarkToolMetricsNotCalledWithoutRoundDoesNotPanic(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status: "completed",
		ToolCallDecision: "not_called",
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolCallDecision != "not_called" || metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("metrics = %#v, want explicit decision with unavailable execution timing", metrics)
	}
}

func TestBuildBenchmarkToolMetricsFailedJobDoesNotExposeNotCalled(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status: "failed",
		ToolCallDecision: "not_called",
		ModelRoundDurationsMS: []int64{125},
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolCallDecision != "unknown" || metrics.ModelFinalGenerationStatus != "unknown" {
		t.Fatalf("metrics = %#v, want unknown for failed job", metrics)
	}
}


func TestBuildBenchmarkToolMetricsPartialAuditLossKeepsExecutionUnknown(t *testing.T) {
	job := benchmarkJobSnapshot{
		Scenario: "tool_performance_auto",
		Status: "completed",
		ToolCallDecision: "called",
		ToolCallEventCount: 2,
		ModelRoundDurationsMS: []int64{125, 230},
	}
	events := []store.ToolCallEvent{{Round: 1, Status: "succeeded", DurationMS: 55}}
	metrics := buildBenchmarkToolMetrics(job, events, true)
	if metrics.ToolCallDecision != "called" || metrics.ToolCalls != 2 {
		t.Fatalf("decision/call count = %q/%d, want called/2", metrics.ToolCallDecision, metrics.ToolCalls)
	}
	if metrics.ToolExecutionAttempts != 0 || metrics.ToolExecutionSuccessRate != nil || metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("execution metrics = %#v, want unknown when persisted audit count is incomplete", metrics)
	}
}
