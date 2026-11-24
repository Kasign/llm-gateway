package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func containsBenchmarkTask(tasks []BenchmarkTask, taskID string) bool {
	for _, task := range tasks {
		if task.TaskID == taskID {
			return true
		}
	}
	return false
}

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

	toolID := id + "-tool"
	zeroFinished := now.Add(time.Second)
	toolTask := BenchmarkTask{TaskID: toolID, Scenario: "tool_performance", Status: "completed", ConfigJSON: `{"condition_status":"partial","condition_missing":["backend_version"]}`, RunCount: 1, StartedAt: now, FinishedAt: &zeroFinished, LatencyMS: 0}
	toolRun := BenchmarkRun{RunID: toolID, TaskID: toolID, RunNo: 1, RequestID: toolID, Status: "completed", StartedAt: now}
	if err := repo.CreateTaskAndRun(ctx, toolTask, toolRun); err != nil {
		t.Fatalf("create tool task/run: %v", err)
	}
	defer func() { _, _ = db.ExecContext(context.Background(), "DELETE FROM benchmark_tasks WHERE task_id=?", toolID) }()
	listed, total, err := repo.ListTasks(ctx, "tool_performance", "completed", 10, 0)
	if err != nil || total < 1 || len(listed) == 0 || !containsBenchmarkTask(listed, toolID) {
		t.Fatalf("filtered task list = %+v, total=%d, err=%v", listed, total, err)
	}
	for _, listedTask := range listed {
		if listedTask.TaskID == toolID && (!listedTask.LatencyMeasured || listedTask.LatencyMS != 0) {
			t.Fatalf("zero latency must remain a measured value: %+v", listedTask)
		}
	}
	page, pageTotal, err := repo.ListTasks(ctx, "", "", 1, 1)
	if err != nil || pageTotal < 2 || len(page) != 1 {
		t.Fatalf("paginated task list = %+v, total=%d, err=%v", page, pageTotal, err)
	}
	if _, _, err := repo.ListTasks(ctx, "", "", 101, 0); err == nil {
		t.Fatal("ListTasks must reject limit above 100")
	}
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


func TestBenchmarkExperimentRepoMariaDB(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN is required for MariaDB Benchmark experiment repository integration")
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
		t.Fatalf("refusing Benchmark experiment test against non-test database %q", databaseName)
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

	now := time.Now()
	id := fmt.Sprintf("experiment-test-%d", now.UnixNano())
	repo := NewBenchmarkExperimentRepo(db)
	experiment := BenchmarkExperiment{
		ExperimentID: id, Name: "线程数单变量对照", Status: "draft", ChangedParameter: "n_threads",
		BaselineValueJSON: "4", CandidateValueJSON: "6",
		BaselineConditionJSON: `{"model":"test-model","prompt_sha256":"abc"}`,
		CandidateConditionJSON: `{"model":"test-model","prompt_sha256":"abc"}`,
		BaselineJobIDsJSON: "[]", CandidateJobIDsJSON: "[]", PlannedSamples: 3,
	}
	if err := repo.Create(ctx, experiment); err != nil { t.Fatalf("create experiment: %v", err) }
	defer func(){ _, _ = db.ExecContext(context.Background(),"DELETE FROM benchmark_experiments WHERE experiment_id=?",id) }()

	if err := repo.AddJob(ctx,id,"baseline","baseline-job-1"); err != nil { t.Fatalf("add baseline job: %v",err) }
	if err := repo.AddJob(ctx,id,"baseline","baseline-job-1"); err != nil { t.Fatalf("repeat baseline job association: %v",err) }
	if err := repo.AddJob(ctx,id,"candidate","candidate-job-1"); err != nil { t.Fatalf("add candidate job: %v",err) }
	got,err:=repo.Get(ctx,id)
	if err!=nil { t.Fatalf("get experiment: %v",err) }
	var baselineIDs,candidateIDs []string
	if err:=json.Unmarshal([]byte(got.BaselineJobIDsJSON),&baselineIDs);err!=nil { t.Fatalf("decode baseline jobs: %v",err) }
	if err:=json.Unmarshal([]byte(got.CandidateJobIDsJSON),&candidateIDs);err!=nil { t.Fatalf("decode candidate jobs: %v",err) }
	if len(baselineIDs)!=1||baselineIDs[0]!="baseline-job-1"||len(candidateIDs)!=1||candidateIDs[0]!="candidate-job-1" {
		t.Fatalf("unexpected experiment job associations: baseline=%v candidate=%v",baselineIDs,candidateIDs)
	}
	listed,total,err:=repo.List(ctx,"draft",10,0)
	if err!=nil||total<1||len(listed)==0 { t.Fatalf("list experiments: total=%d rows=%d err=%v",total,len(listed),err) }
	if _,_,err:=repo.List(ctx,"draft",101,0);err==nil { t.Fatal("experiment list must reject limit above 100") }
	if err:=repo.UpdateState(ctx,id,"completed",`{"qualification":"not_comparable","sample_count":2}`,"","",nil,&now);err!=nil {
		t.Fatalf("update experiment state: %v",err)
	}
	got,err=repo.Get(ctx,id)
	if err!=nil||got.Status!="completed"||got.ReportJSON==""||got.FinishedAt==nil {
		t.Fatalf("updated experiment = %+v err=%v",got,err)
	}
	// 继续实验时只更新状态，不应清空上一轮已持久化的比较报告。
	if err:=repo.UpdateState(ctx,id,"completed","","","",nil,&now);err!=nil{t.Fatalf("update state without report: %v",err)}
	got,err=repo.Get(ctx,id)
	if err!=nil||got.ReportJSON==""{t.Fatalf("state-only update must preserve previous report: %+v err=%v",got,err)}
	// Gateway 重启后，运行中的实验必须进入 interrupted，已完成实验不能被覆盖。
	runningID := id + "-running"
	runningExperiment := experiment
	runningExperiment.ExperimentID = runningID
	runningExperiment.Name = "重启恢复状态验证"
	runningExperiment.Status = "running"
	if err := repo.Create(ctx, runningExperiment); err != nil { t.Fatalf("create running experiment: %v", err) }
	defer func(){ _, _ = db.ExecContext(context.Background(),"DELETE FROM benchmark_experiments WHERE experiment_id=?",runningID) }()
	interruptedCount, err := repo.MarkRunningInterrupted(ctx)
	if err != nil || interruptedCount != 1 {
		t.Fatalf("mark running experiments interrupted: count=%d err=%v", interruptedCount, err)
	}
	recovered, err := repo.Get(ctx, runningID)
	if err != nil || recovered.Status != "interrupted" || recovered.ErrorCode != "gateway_restarted" || recovered.FinishedAt == nil {
		t.Fatalf("recovered experiment = %+v err=%v", recovered, err)
	}
	if err:=repo.AddJob(ctx,id,"invalid","job");err==nil { t.Fatal("invalid experiment group must be rejected") }
	if err:=repo.Create(ctx,BenchmarkExperiment{ExperimentID:id+"-invalid",Name:"bad",ChangedParameter:"x",BaselineValueJSON:"{",CandidateValueJSON:"1"});err==nil {
		t.Fatal("invalid JSON snapshot must be rejected")
	}
	// 实验关联是追溯引用而非级联外键：清理 Benchmark Task 不应删除研究记录。
	taskID:=id+"-task"
	task:=BenchmarkTask{TaskID:taskID,Scenario:"performance",Status:"completed",StartedAt:now,FinishedAt:&now}
	run:=BenchmarkRun{RunID:taskID,TaskID:taskID,RunNo:1,RequestID:taskID,Status:"completed",StartedAt:now,FinishedAt:&now}
	if err:=NewBenchmarkRepo(db).CreateTaskAndRun(ctx,task,run);err!=nil { t.Fatalf("create referenced benchmark task: %v",err) }
	if _,err:=db.ExecContext(ctx,"DELETE FROM benchmark_tasks WHERE task_id=?",taskID);err!=nil { t.Fatalf("delete referenced benchmark task: %v",err) }
	if _,err:=repo.Get(ctx,id);err!=nil { t.Fatalf("experiment must survive benchmark task cleanup: %v",err) }

	// 删除实验只清理实验元数据；活动实验必须被拒绝，Benchmark Task/Run 必须保留。
	activeDeleteID := id + "-active-delete"
	activeDelete := experiment
	activeDelete.ExperimentID = activeDeleteID
	activeDelete.Name = "活动实验禁止删除"
	activeDelete.Status = "running"
	if err := repo.Create(ctx, activeDelete); err != nil { t.Fatalf("create active delete fixture: %v", err) }
	if err := repo.Delete(ctx, activeDeleteID); err != ErrBenchmarkExperimentActive {
		t.Fatalf("deleting running experiment error = %v, want ErrBenchmarkExperimentActive", err)
	}
	if err := repo.UpdateState(ctx, activeDeleteID, "interrupted", "", "", "", nil, &now); err != nil {
		t.Fatalf("mark active delete fixture interrupted: %v", err)
	}
	if err := repo.Delete(ctx, activeDeleteID); err != nil {
		t.Fatalf("delete interrupted experiment: %v", err)
	}
	if _, err := repo.Get(ctx, activeDeleteID); err != sql.ErrNoRows {
		t.Fatalf("deleted experiment lookup error = %v, want sql.ErrNoRows", err)
	}

	deleteID := id + "-preserve-benchmark"
	deleteTaskID := deleteID + "-job"
	deleteExperiment := experiment
	deleteExperiment.ExperimentID = deleteID
	deleteExperiment.Name = "删除实验保留 Benchmark"
	if err := repo.Create(ctx, deleteExperiment); err != nil { t.Fatalf("create delete fixture: %v", err) }
	deleteTask := BenchmarkTask{TaskID: deleteTaskID, Scenario: "performance", Status: "completed", StartedAt: now, FinishedAt: &now}
	deleteRun := BenchmarkRun{RunID: deleteTaskID, TaskID: deleteTaskID, RunNo: 1, RequestID: deleteTaskID, Status: "completed", StartedAt: now, FinishedAt: &now}
	if err := NewBenchmarkRepo(db).CreateTaskAndRun(ctx, deleteTask, deleteRun); err != nil { t.Fatalf("create benchmark task to preserve: %v", err) }
	if err := repo.AddJob(ctx, deleteID, "baseline", deleteTaskID); err != nil { t.Fatalf("associate benchmark task to delete fixture: %v", err) }
	if err := repo.Delete(ctx, deleteID); err != nil { t.Fatalf("delete experiment metadata: %v", err) }
	if _, err := repo.Get(ctx, deleteID); err != sql.ErrNoRows { t.Fatalf("deleted experiment lookup error = %v, want sql.ErrNoRows", err) }
	if _, err := NewBenchmarkRepo(db).GetTask(ctx, deleteTaskID); err != nil { t.Fatalf("Benchmark Task must survive experiment deletion: %v", err) }
	_, _ = db.ExecContext(ctx, "DELETE FROM benchmark_tasks WHERE task_id=?", deleteTaskID)
}
