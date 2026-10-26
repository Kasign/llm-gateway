// RequestStats 保存请求审计统计结果。统计只返回聚合值，不包含请求正文、密钥或单条调用信息。
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type RequestStats struct {
	Range     string               `json:"range"`
	From      time.Time            `json:"from"`
	To        time.Time            `json:"to"`
	ProviderID string              `json:"provider_id,omitempty"`
	Requests  RequestStatsRequests `json:"requests"`
	Latency   RequestStatsLatency  `json:"latency"`
	Tokens    RequestStatsTokens   `json:"tokens"`
}

type RequestStatsRequests struct {
	Total              int64   `json:"total_requests"`
	Completed          int64   `json:"completed_requests"`
	Success            int64   `json:"success_requests"`
	Errors             int64   `json:"error_requests"`
	Cancelled          int64   `json:"cancelled_requests"`
	Running            int64   `json:"running_requests"`
	SuccessRatePercent float64 `json:"success_rate_percent"`
}

type RequestStatsLatency struct {
	SampleCount int64                   `json:"sample_count"`
	AverageMS   *float64                `json:"average_ms,omitempty"`
	Buckets     []RequestStatsLatBucket `json:"buckets"`
}

type RequestStatsLatBucket struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type RequestStatsTokens struct {
	UpstreamUsage RequestStatsTokenSource `json:"upstream_usage"`
	LlamaTimings  RequestStatsTokenSource `json:"llama_timings"`
	UnknownSource RequestStatsTokenSource `json:"unknown_source"`
	Note          string                 `json:"note"`
}

type RequestStatsTokenSource struct {
	Requests     int64 `json:"requests"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// AggregateRequestStats 按开始时间聚合审计记录。providerID 为空表示统计全部来源；窗口边界为 [from, to)。
func (r *Repo) AggregateRequestStats(ctx context.Context, from, to time.Time, providerID string) (*RequestStats, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	query := `SELECT
		COUNT(*),
		COALESCE(SUM(status='success'),0),
		COALESCE(SUM(status='error'),0),
		COALESCE(SUM(status='cancelled'),0),
		COALESCE(SUM(status='running'),0),
		AVG(CASE WHEN status<>'running' AND latency_ms IS NOT NULL THEN latency_ms END),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms<1000),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms>=1000 AND latency_ms<3000),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms>=3000 AND latency_ms<10000),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms>=10000 AND latency_ms<30000),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms>=30000 AND latency_ms<60000),0),
		COALESCE(SUM(status<>'running' AND latency_ms IS NOT NULL AND latency_ms>=60000),0),
		COALESCE(SUM(token_source='upstream_usage' AND has_tokens=1),0),
		COALESCE(SUM(CASE WHEN token_source='upstream_usage' THEN COALESCE(input_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='upstream_usage' THEN COALESCE(output_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='upstream_usage' THEN COALESCE(total_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(token_source='llama_timings' AND has_tokens=1),0),
		COALESCE(SUM(CASE WHEN token_source='llama_timings' THEN COALESCE(input_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='llama_timings' THEN COALESCE(output_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='llama_timings' THEN COALESCE(total_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(token_source='unknown' AND has_tokens=1),0),
		COALESCE(SUM(CASE WHEN token_source='unknown' THEN COALESCE(input_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='unknown' THEN COALESCE(output_tokens,0) ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN token_source='unknown' THEN COALESCE(total_tokens,0) ELSE 0 END),0)
	FROM (
		SELECT status, latency_ms, input_tokens, output_tokens, total_tokens,
			CASE
				WHEN JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(analysis_json),analysis_json,'{}'), '$.throughput.source')) IN ('usage','upstream_usage') THEN 'upstream_usage'
				WHEN JSON_UNQUOTE(JSON_EXTRACT(IF(JSON_VALID(analysis_json),analysis_json,'{}'), '$.throughput.source'))='llama_timings' THEN 'llama_timings'
				ELSE 'unknown'
			END AS token_source,
			CASE WHEN input_tokens IS NOT NULL OR output_tokens IS NOT NULL OR total_tokens IS NOT NULL THEN 1 ELSE 0 END AS has_tokens
		FROM request_logs
		WHERE started_at>=? AND started_at<?`
	args := []interface{}{from, to}
	if providerID != "" {
		query += " AND provider_id=?"
		args = append(args, providerID)
	}
	query += ") AS request_stats_scope"

	var (
		total, success, errors, cancelled, running int64
		avgLatency sql.NullFloat64
		sampleCount int64
		buckets [6]int64
		upstream, timings, unknown [4]int64
	)
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&total, &success, &errors, &cancelled, &running,
		&avgLatency, &sampleCount,
		&buckets[0], &buckets[1], &buckets[2], &buckets[3], &buckets[4], &buckets[5],
		&upstream[0], &upstream[1], &upstream[2], &upstream[3],
		&timings[0], &timings[1], &timings[2], &timings[3],
		&unknown[0], &unknown[1], &unknown[2], &unknown[3],
	)
	if err != nil {
		return nil, err
	}

	completed := total - running
	if completed < 0 {
		completed = 0
	}
	var successRate float64
	if completed > 0 {
		successRate = float64(success) * 100 / float64(completed)
	}
	stats := &RequestStats{
		From: from,
		To: to,
		ProviderID: providerID,
		Requests: RequestStatsRequests{
			Total: total, Completed: completed, Success: success, Errors: errors,
			Cancelled: cancelled, Running: running, SuccessRatePercent: successRate,
		},
		Latency: RequestStatsLatency{
			SampleCount: sampleCount,
			Buckets: []RequestStatsLatBucket{
				{Label: "< 1 秒", Count: buckets[0]},
				{Label: "1–3 秒", Count: buckets[1]},
				{Label: "3–10 秒", Count: buckets[2]},
				{Label: "10–30 秒", Count: buckets[3]},
				{Label: "30–60 秒", Count: buckets[4]},
				{Label: "≥ 60 秒", Count: buckets[5]},
			},
		},
		Tokens: RequestStatsTokens{
			UpstreamUsage: RequestStatsTokenSource{Requests: upstream[0], InputTokens: upstream[1], OutputTokens: upstream[2], TotalTokens: upstream[3]},
			LlamaTimings: RequestStatsTokenSource{Requests: timings[0], InputTokens: timings[1], OutputTokens: timings[2], TotalTokens: timings[3]},
			UnknownSource: RequestStatsTokenSource{Requests: unknown[0], InputTokens: unknown[1], OutputTokens: unknown[2], TotalTokens: unknown[3]},
			Note: "仅按 analysis_json.throughput.source 分类；缺少可识别来源的旧记录列入 unknown_source，不推测为上游 usage。llama_timings 为本地引擎计数，不等同于供应商计费 usage。",
		},
	}
	if avgLatency.Valid {
		v := avgLatency.Float64
		stats.Latency.AverageMS = &v
	}
	return stats, nil
}
