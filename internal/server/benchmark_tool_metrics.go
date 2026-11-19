package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

type benchmarkToolMetricsContextKey struct{}

func withBenchmarkToolMetrics(ctx context.Context, job *benchmarkJob) context.Context {
	if ctx == nil || job == nil { return ctx }
	return context.WithValue(ctx, benchmarkToolMetricsContextKey{}, job)
}

func benchmarkToolRoundObserver(ctx context.Context) func(time.Duration) {
	if ctx == nil { return nil }
	job, _ := ctx.Value(benchmarkToolMetricsContextKey{}).(*benchmarkJob)
	if job == nil { return nil }
	return job.recordModelRoundDuration
}

func decodeBenchmarkRoundDurations(raw string) []int64 {
	if raw == "" { return nil }
	var values []int64
	if err := json.Unmarshal([]byte(raw), &values); err != nil { return nil }
	return values
}


// benchmarkToolMetrics 是服务端计算的工具场景阶段指标，前端只负责展示，不重新计算。
type benchmarkToolMetrics struct {
	ToolChoiceMode string `json:"tool_choice_mode"`
	ToolCallDecision string `json:"tool_call_decision"`
	ToolExecutionAttempts int `json:"tool_execution_attempts"`
	ToolExecutionSuccesses int `json:"tool_execution_successes"`
	ToolExecutionFailures int `json:"tool_execution_failures"`
	ToolExecutionSuccessRate *float64 `json:"tool_execution_success_rate,omitempty"`
	ModelRounds int `json:"model_rounds"`
	ToolRounds int `json:"tool_rounds"`
	ToolCalls int `json:"tool_calls"`
	ModelDecisionMS *int64 `json:"model_decision_ms,omitempty"`
	ModelDecisionStatus string `json:"model_decision_status"`
	ToolExecutionTotalMS *int64 `json:"tool_execution_total_ms,omitempty"`
	ToolExecutionStatus string `json:"tool_execution_status"`
	ModelFinalGenerationMS *int64 `json:"model_final_generation_ms,omitempty"`
	ModelFinalGenerationStatus string `json:"model_final_generation_status"`
}

func buildBenchmarkToolMetrics(job benchmarkJobSnapshot, events []store.ToolCallEvent, eventsAvailable bool) benchmarkToolMetrics {
	metrics := benchmarkToolMetrics{
		ToolChoiceMode: "not_applicable",
		ToolCallDecision: "not_applicable",
		ModelDecisionStatus: "not_applicable",
		ToolExecutionStatus: "not_applicable",
		ModelFinalGenerationStatus: "not_applicable",
	}
	if !isBenchmarkToolScenario(job.Scenario) {
		return metrics
	}
	metrics.ToolChoiceMode = benchmarkToolChoiceMode(job.Scenario)
	metrics.ToolCallDecision = "unknown"
	metrics.ModelDecisionStatus = "unknown"
	metrics.ToolExecutionStatus = "unknown"
	metrics.ModelFinalGenerationStatus = "unknown"
	metrics.ModelRounds = len(job.ModelRoundDurationsMS)
	if len(job.ModelRoundDurationsMS) > 0 {
		v := job.ModelRoundDurationsMS[0]
		metrics.ModelDecisionMS = &v
		metrics.ModelDecisionStatus = "measured"
	}

	seenRounds := make(map[int]struct{}, len(events))
	var total int64
	for _, event := range events {
		seenRounds[event.Round] = struct{}{}
		switch event.Status {
		case "succeeded":
			metrics.ToolExecutionAttempts++
			metrics.ToolExecutionSuccesses++
			if event.DurationMS > 0 && total <= int64(^uint64(0)>>1)-event.DurationMS {
				total += event.DurationMS
			}
		case "failed", "timed_out", "cancelled":
			metrics.ToolExecutionAttempts++
			metrics.ToolExecutionFailures++
			if event.DurationMS > 0 && total <= int64(^uint64(0)>>1)-event.DurationMS {
				total += event.DurationMS
			}
		}
	}
	if len(events) > 0 {
		metrics.ToolCallDecision = "called"
	} else if job.Status == "completed" && eventsAvailable && len(job.ModelRoundDurationsMS) == 1 {
		metrics.ToolCallDecision = "not_called"
	}
	if metrics.ToolExecutionAttempts > 0 {
		rate := float64(metrics.ToolExecutionSuccesses) / float64(metrics.ToolExecutionAttempts) * 100
		metrics.ToolExecutionSuccessRate = &rate
	}
	metrics.ToolCalls = len(events)
	metrics.ToolRounds = len(seenRounds)
	if metrics.ToolExecutionAttempts > 0 {
		v := total
		metrics.ToolExecutionTotalMS = &v
		metrics.ToolExecutionStatus = "measured"
	} else if job.Status == "completed" && eventsAvailable && len(job.ModelRoundDurationsMS) == 1 && metrics.ToolChoiceMode == "auto" && len(events) == 0 {
		// 自然决策模式下模型可以合理地选择不调用工具；第一轮响应同时包含决策与最终回答，
		// 无法从单次模型请求中拆分两段耗时，因此显式标记为合并测量，而不是不适用或重复计时。
		metrics.ToolExecutionStatus = "not_applicable"
		v := job.ModelRoundDurationsMS[0]
		metrics.ModelFinalGenerationMS = &v
		metrics.ModelFinalGenerationStatus = "combined_with_decision"
	}
	if job.Status == "completed" && metrics.ToolExecutionAttempts > 0 && len(job.ModelRoundDurationsMS) >= 2 {
		v := job.ModelRoundDurationsMS[len(job.ModelRoundDurationsMS)-1]
		metrics.ModelFinalGenerationMS = &v
		metrics.ModelFinalGenerationStatus = "measured"
	}
	return metrics
}
