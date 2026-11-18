package server

import (
	"context"
	"encoding/json"
	"time"
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
