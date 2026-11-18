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

func TestBenchmarkRepoMariaDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is required for MariaDB Benchmark repository integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("mysql", dsn)
	if err != nil { t.Fatalf("open MariaDB: %v", err) }
	defer db.Close()
	if err := db.PingContext(ctx); err != nil { t.Fatalf("ping MariaDB: %v", err) }
	var databaseName string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&databaseName); err != nil { t.Fatalf("read database: %v", err) }
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		t.Fatalf("refusing Benchmark repository test against non-test database %q", databaseName)
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok { t.Fatal("resolve source file for migration schema") }
	schema, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "../../migrations/001_init.sql"))
	if err != nil { t.Fatalf("read migration schema: %v", err) }
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" { continue }
		if _, err := db.ExecContext(ctx, statement); err != nil { t.Fatalf("apply test schema: %v", err) }
	}
	repo := NewBenchmarkRepo(db)
	id := fmt.Sprintf("bench-test-%d", time.Now().UnixNano())
	now := time.Now()
	task := BenchmarkTask{
		TaskID: id, Scenario: "performance", Status: "running", PromptSHA256: strings.Repeat("a", 64),
		PromptLength: 32, MaxTokens: 256, ConfigJSON: `{"temperature":0.2}`, RunCount: 1, StartedAt: now,
	}
	run := BenchmarkRun{RunID: id, TaskID: id, RunNo: 1, RequestID: id, Status: "running", StartedAt: now}
	if err := repo.CreateTaskAndRun(ctx, task, run); err != nil { t.Fatalf("create task/run: %v", err) }
	defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM benchmark_tasks WHERE task_id=?", id) }()
	got, err := repo.GetTask(ctx, id)
	if err != nil { t.Fatalf("get task: %v", err) }
	if got.TaskID != id || got.Status != "running" || got.PromptLength != 32 || got.ConfigJSON != task.ConfigJSON {
		t.Fatalf("unexpected task: %+v", got)
	}
	runs, err := repo.ListRuns(ctx, id)
	if err != nil { t.Fatalf("list runs: %v", err) }
	if len(runs) != 1 || runs[0].RequestID != id || runs[0].RunNo != 1 {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	// 取消请求必须在同一事务中转换 Task 与 Run，重复取消不能覆盖状态。
	changed, err := repo.MarkTaskCancelling(ctx, id)
	if err != nil || !changed {
		t.Fatalf("mark task cancelling = %v, err=%v", changed, err)
	}
	got, err = repo.GetTask(ctx, id)
	if err != nil || got.Status != "cancelling" {
		t.Fatalf("cancelling task = %+v, err=%v", got, err)
	}
	runs, err = repo.ListRuns(ctx, id)
	if err != nil || len(runs) != 1 || runs[0].Status != "cancelling" {
		t.Fatalf("cancelling runs = %+v, err=%v", runs, err)
	}
	changed, err = repo.MarkTaskCancelling(ctx, id)
	if err != nil || changed {
		t.Fatalf("duplicate cancellation = %v, err=%v; want false, nil", changed, err)
	}
	finished := time.Now()
	latency := int64(1200)
	task.Status, task.FinishedAt, task.LatencyMS = "completed", &finished, latency
	run.Status, run.FinishedAt, run.LatencyMS, run.HTTPStatus = "completed", &finished, latency, 200
	modelLoad, inference := int64(321), int64(0)
	run.ModelLoadMS, run.ModelLoadStatus = &modelLoad, "measured"
	run.InferenceLatencyMS, run.InferenceLatencyStatus = &inference, "measured"
	run.TTFTStatus = "measured"
	run.ModelRoundDurationsJSON = "[12,34]"
	if err := repo.UpdateTaskAndRun(ctx, task, run); err != nil { t.Fatalf("update task/run: %v", err) }
	runs, err = repo.ListRuns(ctx, id)
	if err != nil || len(runs) != 1 || runs[0].ModelLoadMS == nil || *runs[0].ModelLoadMS != modelLoad || runs[0].ModelLoadStatus != "measured" ||
		runs[0].InferenceLatencyMS == nil || *runs[0].InferenceLatencyMS != inference || runs[0].InferenceLatencyStatus != "measured" || runs[0].TTFTStatus != "measured" || runs[0].ModelRoundDurationsJSON != "[12,34]" {
		t.Fatalf("persisted timing metrics = %+v, err=%v", runs, err)
	}
	got, err = repo.GetTask(ctx, id)
	if err != nil || got.Status != "completed" || got.LatencyMS != latency || got.FinishedAt == nil {
		t.Fatalf("updated task = %+v, err=%v", got, err)
	}
	changed, err = repo.MarkTaskCancelling(ctx, id)
	if err != nil || changed {
		t.Fatalf("terminal task cancellation = %v, err=%v; want false, nil", changed, err)
	}
	// 单独验证活动任务恢复；创建第二条活动任务后 MarkInterrupted 应同时更新 Task 与 Run。
	interruptedID := id + "-restart"
	task2 := BenchmarkTask{TaskID: interruptedID, Scenario: "performance", Status: "running", StartedAt: now}
	run2 := BenchmarkRun{RunID: interruptedID, TaskID: interruptedID, RunNo: 1, RequestID: interruptedID, Status: "running", StartedAt: now}
	if err := repo.CreateTaskAndRun(ctx, task2, run2); err != nil { t.Fatalf("create restart task: %v", err) }
	defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM benchmark_tasks WHERE task_id=?", interruptedID) }()
	if _, err := repo.MarkInterrupted(ctx); err != nil { t.Fatalf("mark interrupted: %v", err) }
	got2, err := repo.GetTask(ctx, interruptedID)
	if err != nil || got2.Status != "interrupted" || got2.ErrorCode != "gateway_restarted" {
		t.Fatalf("interrupted task = %+v, err=%v", got2, err)
	}
	runs2, err := repo.ListRuns(ctx, interruptedID)
	if err != nil || len(runs2) != 1 || runs2[0].Status != "interrupted" ||
		runs2[0].ModelLoadMS != nil || runs2[0].ModelLoadStatus != "unknown" ||
		runs2[0].InferenceLatencyMS != nil || runs2[0].InferenceLatencyStatus != "unknown" || runs2[0].TTFTStatus != "unknown" {
		t.Fatalf("interrupted runs = %+v, err=%v", runs2, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM benchmark_tasks WHERE task_id=?", interruptedID); err != nil {
		t.Fatalf("delete task and cascade run: %v", err)
	}
	if _, err := repo.GetTask(ctx, interruptedID); err != sql.ErrNoRows {
		t.Fatalf("deleted task lookup error = %v, want sql.ErrNoRows", err)
	}
	var remainingRuns int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM benchmark_runs WHERE task_id=?", interruptedID).Scan(&remainingRuns); err != nil {
		t.Fatalf("count cascaded runs: %v", err)
	}
	if remainingRuns != 0 {
		t.Fatalf("cascaded run count = %d, want 0", remainingRuns)
	}

	// 取消事务与自然完成并发时，最终终态必须由完成事务保留。
	raceID := id + "-cancel-finish-race"
	raceTask := BenchmarkTask{TaskID: raceID, Scenario: "performance", Status: "running", StartedAt: now}
	raceRun := BenchmarkRun{RunID: raceID, TaskID: raceID, RunNo: 1, RequestID: raceID, Status: "running", StartedAt: now}
	if err := repo.CreateTaskAndRun(ctx, raceTask, raceRun); err != nil {
		t.Fatalf("create race task: %v", err)
	}
	defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM benchmark_tasks WHERE task_id=?", raceID) }()
	startRace := make(chan struct{})
	raceErrors := make(chan error, 2)
	go func() {
		<-startRace
		_, err := repo.MarkTaskCancelling(ctx, raceID)
		raceErrors <- err
	}()
	go func() {
		<-startRace
		finished := time.Now()
		raceTask.Status, raceTask.FinishedAt = "completed", &finished
		raceRun.Status, raceRun.FinishedAt = "completed", &finished
		raceErrors <- repo.UpdateTaskAndRun(ctx, raceTask, raceRun)
	}()
	close(startRace)
	for i := 0; i < 2; i++ {
		if err := <-raceErrors; err != nil {
			t.Fatalf("concurrent cancel/finish update: %v", err)
		}
	}
	raceGot, err := repo.GetTask(ctx, raceID)
	if err != nil || raceGot.Status != "completed" {
		t.Fatalf("race task = %+v, err=%v; want completed", raceGot, err)
	}
	raceRuns, err := repo.ListRuns(ctx, raceID)
	if err != nil || len(raceRuns) != 1 || raceRuns[0].Status != "completed" {
		t.Fatalf("race runs = %+v, err=%v; want completed", raceRuns, err)
	}
}
