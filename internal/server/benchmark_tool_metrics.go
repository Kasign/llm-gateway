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

func buildBenchmarkToolMetrics(job benchmarkJobSnapshot, events []store.ToolCallEvent) benchmarkToolMetrics {
	metrics := benchmarkToolMetrics{
		ModelDecisionStatus: "not_applicable",
		ToolExecutionStatus: "not_applicable",
		ModelFinalGenerationStatus: "not_applicable",
	}
	if job.Scenario != "tool_performance" {
		return metrics
	}
	metrics.ModelDecisionStatus = "unknown"
	metrics.ToolExecutionStatus = "unknown"
	metrics.ModelFinalGenerationStatus = "unknown"
	metrics.ModelRounds = len(job.ModelRoundDurationsMS)
	if len(job.ModelRoundDurationsMS) > 0 {
		v := job.ModelRoundDurationsMS[0]
		metrics.ModelDecisionMS = &v
		metrics.ModelDecisionStatus = "measured"
	}
	if len(events) > 0 {
		seenRounds := make(map[int]struct{}, len(events))
		var total int64
		for _, event := range events {
			seenRounds[event.Round] = struct{}{}
			if event.DurationMS > 0 && total <= int64(^uint64(0)>>1)-event.DurationMS {
				total += event.DurationMS
			}
		}
		v := total
		metrics.ToolExecutionTotalMS = &v
		metrics.ToolExecutionStatus = "measured"
		metrics.ToolCalls = len(events)
		metrics.ToolRounds = len(seenRounds)
	}
	if job.Status == "completed" && len(job.ModelRoundDurationsMS) >= 2 {
		v := job.ModelRoundDurationsMS[len(job.ModelRoundDurationsMS)-1]
		metrics.ModelFinalGenerationMS = &v
		metrics.ModelFinalGenerationStatus = "measured"
	}
	return metrics
}
