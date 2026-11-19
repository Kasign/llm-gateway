package db_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/whl/llm-gateway/internal/db"
	"github.com/whl/llm-gateway/internal/store"
)

// TestMariaDBBenchmarkDecisionMigration 需要指向专用 MariaDB 测试账号的
// MARIADB_TEST_DSN；测试会创建并删除随机命名的独立 schema，不清理现有 schema。
func TestMariaDBBenchmarkDecisionMigration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MARIADB_TEST_DSN"))
	if dsn == "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("CI")), "true") {
			t.Fatal("MARIADB_TEST_DSN is required in CI; refusing to skip MariaDB migration integration test")
		}
		t.Skip("set MARIADB_TEST_DSN to run isolated MariaDB migration integration test")
	}

	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse MARIADB_TEST_DSN: %v", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate isolated schema name: %v", err)
	}
	schema := "llm_gateway_benchmark_test_" + hex.EncodeToString(suffix)

	adminCfg := *cfg
	adminCfg.DBName = ""
	adminDB, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatalf("open MariaDB admin connection: %v", err)
	}
	defer adminDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+schema+" CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatalf("create isolated schema %s: %v", schema, err)
	}
	defer func() {
		if _, err := adminDB.ExecContext(context.Background(), "DROP DATABASE "+schema); err != nil {
			t.Errorf("drop isolated schema %s: %v", schema, err)
		}
	}()

	testCfg := *cfg
	testCfg.DBName = schema
	testDB, err := sql.Open("mysql", testCfg.FormatDSN())
	if err != nil {
		t.Fatalf("open isolated schema: %v", err)
	}
	defer testDB.Close()
	if err := testDB.PingContext(ctx); err != nil {
		t.Fatalf("ping isolated schema: %v", err)
	}

	migrationPath := filepath.Join("..", "..", "migrations", "001_init.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read initial migration: %v", err)
	}
	legacySQL := string(migration)
	for _, column := range []string{
		"  tool_call_decision VARCHAR(16) NOT NULL DEFAULT 'unknown',\n",
		"  tool_call_event_count INT UNSIGNED NOT NULL DEFAULT 0,\n",
	} {
		if strings.Count(legacySQL, column) != 1 {
			t.Fatalf("legacy fixture expected exactly one occurrence of %q", strings.TrimSpace(column))
		}
		legacySQL = strings.Replace(legacySQL, column, "", 1)
	}
	for _, statement := range strings.Split(legacySQL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := testDB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create legacy schema from initial migration: %v", err)
		}
	}

	now := time.Now().UTC()
	if _, err := testDB.ExecContext(ctx, `INSERT INTO benchmark_tasks
		(task_id,scenario,status,prompt_length,max_tokens,run_count,started_at,created_at,updated_at)
		VALUES('legacy-task','tool_performance_auto','completed',12,64,1,?,?,?)`, now, now, now); err != nil {
		t.Fatalf("insert legacy task: %v", err)
	}
	if _, err := testDB.ExecContext(ctx, `INSERT INTO benchmark_runs
		(run_id,task_id,run_no,request_id,status,started_at,created_at,updated_at)
		VALUES('legacy-run','legacy-task',1,'legacy-request','completed',?,?,?)`, now, now, now); err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}

	if err := db.Migrate(ctx, testDB, "127.0.0.1", 3306, migrationPath); err != nil {
		t.Fatalf("upgrade legacy schema: %v", err)
	}
	if err := db.Migrate(ctx, testDB, "127.0.0.1", 3306, migrationPath); err != nil {
		t.Fatalf("repeat migration must be idempotent: %v", err)
	}

	repo := store.NewBenchmarkRepo(testDB)
	legacyRuns, err := repo.ListRuns(ctx, "legacy-task")
	if err != nil {
		t.Fatalf("read migrated legacy run: %v", err)
	}
	if len(legacyRuns) != 1 || legacyRuns[0].ToolCallDecision != "unknown" || legacyRuns[0].ToolCallEventCount != 0 {
		t.Fatalf("legacy evidence must remain unknown/0 after migration, got %+v", legacyRuns)
	}

	finished := now.Add(time.Second)
	newTask := store.BenchmarkTask{
		TaskID: "roundtrip-task", Scenario: "tool_performance_auto", Status: "running",
		PromptLength: 20, MaxTokens: 128, RunCount: 1, StartedAt: now,
	}
	newRun := store.BenchmarkRun{
		RunID: "roundtrip-run", TaskID: newTask.TaskID, RunNo: 1, RequestID: "roundtrip-request",
		Status: "running", StartedAt: now, ToolCallDecision: "unknown",
	}
	if err := repo.CreateTaskAndRun(ctx, newTask, newRun); err != nil {
		t.Fatalf("create benchmark task/run: %v", err)
	}

	newTask.Status = "completed"
	newRun.Status = "completed"
	newRun.ToolCallDecision = "called"
	newRun.ToolCallEventCount = 2
	newRun.HTTPStatus = 200
	newTask.FinishedAt = &finished
	newRun.FinishedAt = &finished
	if err := repo.UpdateTaskAndRun(ctx, newTask, newRun); err != nil {
		t.Fatalf("update benchmark task/run: %v", err)
	}
	roundTripRuns, err := repo.ListRuns(ctx, newTask.TaskID)
	if err != nil {
		t.Fatalf("list benchmark runs: %v", err)
	}
	if len(roundTripRuns) != 1 || roundTripRuns[0].ToolCallDecision != "called" || roundTripRuns[0].ToolCallEventCount != 2 {
		t.Fatalf("called decision/event count did not round-trip: %+v", roundTripRuns)
	}
	loadedTask, err := repo.GetTask(ctx, newTask.TaskID)
	if err != nil {
		t.Fatalf("get benchmark task: %v", err)
	}
	if loadedTask.Status != "completed" || loadedTask.RunCount != 1 {
		t.Fatalf("benchmark task state did not round-trip: %+v", loadedTask)
	}

	notCalledTask := store.BenchmarkTask{
		TaskID: "not-called-task", Scenario: "tool_performance_auto", Status: "completed",
		PromptLength: 8, MaxTokens: 32, RunCount: 1, StartedAt: now, FinishedAt: &finished,
	}
	notCalledRun := store.BenchmarkRun{
		RunID: "not-called-run", TaskID: notCalledTask.TaskID, RunNo: 1, RequestID: "not-called-request",
		Status: "completed", StartedAt: now, FinishedAt: &finished, ToolCallDecision: "not_called",
	}
	if err := repo.CreateTaskAndRun(ctx, notCalledTask, notCalledRun); err != nil {
		t.Fatalf("create not_called benchmark run: %v", err)
	}
	notCalledRuns, err := repo.ListRuns(ctx, notCalledTask.TaskID)
	if err != nil {
		t.Fatalf("read not_called benchmark run: %v", err)
	}
	if len(notCalledRuns) != 1 || notCalledRuns[0].ToolCallDecision != "not_called" || notCalledRuns[0].ToolCallEventCount != 0 {
		t.Fatalf("not_called decision did not round-trip: %+v", notCalledRuns)
	}

	var decisionDefault string
	if err := testDB.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='benchmark_runs' AND COLUMN_NAME='tool_call_decision'`).Scan(&decisionDefault); err != nil {
		t.Fatalf("read decision column default: %v", err)
	}
	var eventDefault sql.NullString
	if err := testDB.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='benchmark_runs' AND COLUMN_NAME='tool_call_event_count'`).Scan(&eventDefault); err != nil {
		t.Fatalf("read event count column default: %v", err)
	}
	if decisionDefault != "unknown" || !eventDefault.Valid || eventDefault.String != "0" {
		t.Fatalf("unexpected migrated column defaults: decision=%q event_count=%q", decisionDefault, eventDefault.String)
	}
}
