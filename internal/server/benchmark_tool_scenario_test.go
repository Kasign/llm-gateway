package server

import (
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
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
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
	if metrics.ModelFinalGenerationStatus != "not_applicable" {
		t.Fatalf("final generation status = %q, want not_applicable", metrics.ModelFinalGenerationStatus)
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
	metrics := buildBenchmarkToolMetrics(job, events)
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
	}
	metrics := buildBenchmarkToolMetrics(job, nil)
	if metrics.ToolChoiceMode != "required" {
		t.Fatalf("tool_choice_mode = %q, want required", metrics.ToolChoiceMode)
	}
	if metrics.ToolExecutionStatus != "unknown" {
		t.Fatalf("tool execution status = %q, want unknown for failed forced invocation", metrics.ToolExecutionStatus)
	}
}
