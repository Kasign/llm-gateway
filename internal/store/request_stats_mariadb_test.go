package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestAggregateRequestStatsMariaDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is required for MariaDB request statistics integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MariaDB: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping MariaDB: %v", err)
	}
	var databaseName string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&databaseName); err != nil {
		t.Fatalf("read selected database: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		t.Fatalf("refusing request statistics integration test against non-test database %q", databaseName)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source file for migration schema")
	}
	schema, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "../../migrations/001_init.sql"))
	if err != nil {
		t.Fatalf("read migration schema: %v", err)
	}
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("apply test schema: %v", err)
		}
	}
	// 测试可能与其他包共用同一个 MariaDB 测试库；CREATE TABLE IF NOT EXISTS 不会升级旧表。
	var tokenSourceColumnCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'request_logs' AND COLUMN_NAME = 'token_source'`).Scan(&tokenSourceColumnCount); err != nil {
		t.Fatalf("检查 request_logs.token_source: %v", err)
	}
	if tokenSourceColumnCount == 0 {
		if _, err := db.ExecContext(ctx, "ALTER TABLE request_logs ADD COLUMN token_source VARCHAR(32) NULL"); err != nil {
			t.Fatalf("补齐 request_logs.token_source 测试字段: %v", err)
		}
	}

	prefix := fmt.Sprintf("request-stats-%d", time.Now().UnixNano())
	start := time.Now().UTC().Truncate(time.Second)
	from, to := start.Add(-time.Minute), start.Add(time.Minute)
	defer func() {
		_, _ = db.ExecContext(context.Background(), "DELETE FROM request_logs WHERE request_id LIKE ?", prefix+"%")
	}()

	type sample struct {
		suffix, provider, status, analysis string
		input, output, total               any
		started                            time.Time
		latency                            int64
	}
	samples := []sample{
		{suffix: "upstream", provider: "remote-a", status: "success", analysis: `{"throughput":{"source":"usage"}}`, input: int64(10), output: int64(4), total: int64(14), started: start, latency: 1200},
		{suffix: "timings", provider: "remote-a", status: "success", analysis: `{"throughput":{"source":"llama_timings"}}`, input: int64(3), output: int64(2), total: int64(5), started: start, latency: 2500},
		{suffix: "unknown", provider: "remote-a", status: "error", analysis: `{"throughput":{"source":"unrecognized"}}`, input: int64(1), output: int64(0), total: int64(1), started: start, latency: 4000},
		{suffix: "legacy", provider: "remote-a", status: "cancelled", analysis: "", input: nil, output: nil, total: nil, started: start, latency: 0},
		{suffix: "other-provider", provider: "remote-b", status: "success", analysis: `{"throughput":{"source":"usage"}}`, input: int64(100), output: int64(50), total: int64(150), started: start, latency: 500},
		{suffix: "outside-window", provider: "remote-a", status: "success", analysis: `{"throughput":{"source":"usage"}}`, input: int64(900), output: int64(900), total: int64(1800), started: to.Add(time.Second), latency: 500},
	}
	for _, item := range samples {
		_, err := db.ExecContext(ctx, `INSERT INTO request_logs
			(request_id, endpoint, method, provider_id, status, started_at, latency_ms, input_tokens, output_tokens, total_tokens, analysis_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			prefix+"-"+item.suffix, "/v1/chat/completions", "POST", item.provider, item.status,
			item.started, item.latency, item.input, item.output, item.total, item.analysis)
		if err != nil {
			t.Fatalf("insert %s sample: %v", item.suffix, err)
		}
	}

	repo := NewRepo(db)
	finishStarted := start.Add(10 * time.Second)
	if err := repo.CreateRequest(ctx, RequestLog{
		RequestID: prefix + "-finish-path", Endpoint: "/v1/chat/completions", Method: "POST",
		ProviderID: "remote-a", Status: "running", StartedAt: finishStarted,
	}); err != nil {
		t.Fatalf("create finish-path sample: %v", err)
	}
	finishedAt := finishStarted.Add(2 * time.Second)
	one, two, three := int64(1), int64(2), int64(3)
	if err := repo.FinishRequest(ctx, RequestLog{
		RequestID: prefix + "-finish-path", Status: "success", HTTPStatus: 200,
		FinishedAt: &finishedAt, LatencyMS: 2000, InputTokens: &one,
		OutputTokens: &two, TotalTokens: &three, TokenSource: "upstream_usage",
	}); err != nil {
		t.Fatalf("finish request sample: %v", err)
	}
	var persistedSource string
	if err := db.QueryRowContext(ctx, "SELECT token_source FROM request_logs WHERE request_id=?", prefix+"-finish-path").Scan(&persistedSource); err != nil {
		t.Fatalf("read persisted token source: %v", err)
	}
	if persistedSource != "upstream_usage" {
		t.Fatalf("persisted token source = %q, want upstream_usage", persistedSource)
	}
	stats, err := repo.AggregateRequestStats(ctx, from, to, " REMOTE-A ")
	if err != nil {
		t.Fatalf("aggregate request stats: %v", err)
	}
	if stats.Requests.Total != 6 || stats.Requests.Success != 4 || stats.Requests.Errors != 1 || stats.Requests.Cancelled != 1 {
		t.Fatalf("unexpected request counts: %+v", stats.Requests)
	}
	if stats.Tokens.UpstreamUsage.Requests != 3 || stats.Tokens.UpstreamUsage.InputTokens != 13 || stats.Tokens.UpstreamUsage.OutputTokens != 7 || stats.Tokens.UpstreamUsage.TotalTokens != 20 {
		t.Fatalf("unexpected upstream usage totals: %+v", stats.Tokens.UpstreamUsage)
	}
	if stats.Tokens.LlamaTimings.Requests != 1 || stats.Tokens.LlamaTimings.TotalTokens != 5 {
		t.Fatalf("unexpected llama timings totals: %+v", stats.Tokens.LlamaTimings)
	}
	if stats.Tokens.UnknownSource.Requests != 1 || stats.Tokens.UnknownSource.TotalTokens != 1 {
		t.Fatalf("unexpected unknown source totals: %+v", stats.Tokens.UnknownSource)
	}
	if stats.Latency.SampleCount != 6 {
		t.Fatalf("latency sample count = %d, want 6", stats.Latency.SampleCount)
	}
	if stats.ProviderID != "remote-a" {
		t.Fatalf("provider filter normalization = %q, want remote-a", stats.ProviderID)
	}
}
