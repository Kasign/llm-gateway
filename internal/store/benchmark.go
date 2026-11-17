package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// BenchmarkTask 是一次测试任务的持久化摘要；不保存 Prompt/Response 正文。
type BenchmarkTask struct {
	TaskID       string
	APIKeyID     *uint64
	Scenario     string
	Status       string
	ProviderID   string
	Backend      string
	ModelKey     string
	ModelID      string
	PromptSHA256 string
	PromptLength int64
	MaxTokens    int
	ConfigJSON   string
	RunCount     int
	StartedAt    time.Time
	FinishedAt   *time.Time
	LatencyMS    int64
	ErrorCode    string
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// BenchmarkRun 是 Task 下的一次独立模型请求；RequestID 关联现有 request_logs 与事件表。
type BenchmarkRun struct {
	RunID              string
	TaskID             string
	RunNo              int
	RequestID          string
	Status             string
	ProviderID         string
	ModelKey           string
	ModelID            string
	StartedAt          time.Time
	FinishedAt         *time.Time
	LatencyMS          int64
	ModelLoadMS        int64
	InferenceLatencyMS int64
	HTTPStatus         int
	ResponseBytes      int64
	InputTokens        *int64
	OutputTokens       *int64
	TotalTokens        *int64
	TTFTMS             *int64
	StreamEvents       int
	ReasoningChars     int
	ContentChars       int
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// BenchmarkRepo 管理 Benchmark Task/Run 元数据，不存请求/响应正文或流事件。
type BenchmarkRepo struct{ db *sql.DB }

func NewBenchmarkRepo(db *sql.DB) *BenchmarkRepo { return &BenchmarkRepo{db: db} }

func (r *BenchmarkRepo) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("benchmark repository is not configured")
	}
	return nil
}

// CreateTaskAndRun 在同一事务中创建 Task 与首个 Run，任何一步失败都不会留下半条任务。
func (r *BenchmarkRepo) CreateTaskAndRun(ctx context.Context, task BenchmarkTask, run BenchmarkRun) error {
	if err := r.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(task.TaskID) == "" || task.TaskID != run.TaskID ||
		strings.TrimSpace(run.RunID) == "" || strings.TrimSpace(run.RequestID) == "" ||
		run.RunNo < 1 || task.PromptLength < 0 || task.MaxTokens < 0 {
		return fmt.Errorf("invalid benchmark task/run identity or limits")
	}
	now := time.Now()
	if task.StartedAt.IsZero() {
		task.StartedAt = now
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = task.StartedAt
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	if task.RunCount == 0 {
		task.RunCount = 1
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO benchmark_tasks
		(task_id,api_key_id,scenario,status,provider_id,backend,model_key,model_id,prompt_sha256,prompt_length,max_tokens,config_json,run_count,started_at,finished_at,latency_ms,error_code,error_message,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.TaskID, task.APIKeyID, defaultString(task.Scenario, "performance"), defaultString(task.Status, "running"),
		nullIfEmpty(task.ProviderID), nullIfEmpty(task.Backend), nullIfEmpty(task.ModelKey), nullIfEmpty(task.ModelID),
		nullIfEmpty(task.PromptSHA256), task.PromptLength, task.MaxTokens, nullIfEmpty(task.ConfigJSON), task.RunCount,
		task.StartedAt, task.FinishedAt, nullablePositiveInt64(task.LatencyMS), nullIfEmpty(task.ErrorCode),
		nullIfEmpty(task.ErrorMessage), task.CreatedAt, task.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO benchmark_runs
		(run_id,task_id,run_no,request_id,status,provider_id,model_key,model_id,started_at,finished_at,latency_ms,model_load_ms,inference_latency_ms,http_status,response_bytes,input_tokens,output_tokens,total_tokens,ttft_ms,stream_event_count,reasoning_chars,content_chars,error_code,error_message,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.RunID, run.TaskID, run.RunNo, run.RequestID, defaultString(run.Status, "running"),
		nullIfEmpty(run.ProviderID), nullIfEmpty(run.ModelKey), nullIfEmpty(run.ModelID), run.StartedAt, run.FinishedAt,
		nullablePositiveInt64(run.LatencyMS), nullablePositiveInt64(run.ModelLoadMS), nullablePositiveInt64(run.InferenceLatencyMS),
		nullablePositiveInt64(int64(run.HTTPStatus)), nullablePositiveInt64(run.ResponseBytes),
		run.InputTokens, run.OutputTokens, run.TotalTokens, run.TTFTMS, run.StreamEvents, run.ReasoningChars,
		run.ContentChars, nullIfEmpty(run.ErrorCode), nullIfEmpty(run.ErrorMessage), run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateTaskAndRun 原子更新聚合任务和指定 Run，避免两者出现不一致的终态。
func (r *BenchmarkRepo) UpdateTaskAndRun(ctx context.Context, task BenchmarkTask, run BenchmarkRun) error {
	if err := r.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(task.TaskID) == "" || task.TaskID != run.TaskID || strings.TrimSpace(run.RunID) == "" {
		return fmt.Errorf("invalid benchmark task/run identity")
	}
	now := time.Now()
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE benchmark_tasks SET
		status=?,provider_id=?,backend=?,model_key=?,model_id=?,run_count=?,finished_at=?,latency_ms=?,error_code=?,error_message=?,updated_at=?
		WHERE task_id=?`,
		task.Status, nullIfEmpty(task.ProviderID), nullIfEmpty(task.Backend), nullIfEmpty(task.ModelKey), nullIfEmpty(task.ModelID),
		task.RunCount, task.FinishedAt, nullablePositiveInt64(task.LatencyMS), nullIfEmpty(task.ErrorCode),
		nullIfEmpty(task.ErrorMessage), task.UpdatedAt, task.TaskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	res, err = tx.ExecContext(ctx, `UPDATE benchmark_runs SET
		status=?,provider_id=?,model_key=?,model_id=?,finished_at=?,latency_ms=?,model_load_ms=?,inference_latency_ms=?,
		http_status=?,response_bytes=?,input_tokens=?,output_tokens=?,total_tokens=?,ttft_ms=?,stream_event_count=?,
		reasoning_chars=?,content_chars=?,error_code=?,error_message=?,updated_at=?
		WHERE run_id=? AND task_id=?`,
		run.Status, nullIfEmpty(run.ProviderID), nullIfEmpty(run.ModelKey), nullIfEmpty(run.ModelID), run.FinishedAt,
		nullablePositiveInt64(run.LatencyMS), nullablePositiveInt64(run.ModelLoadMS), nullablePositiveInt64(run.InferenceLatencyMS),
		nullablePositiveInt64(int64(run.HTTPStatus)), nullablePositiveInt64(run.ResponseBytes),
		run.InputTokens, run.OutputTokens, run.TotalTokens, run.TTFTMS, run.StreamEvents,
		run.ReasoningChars, run.ContentChars, nullIfEmpty(run.ErrorCode), nullIfEmpty(run.ErrorMessage),
		run.UpdatedAt, run.RunID, run.TaskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (r *BenchmarkRepo) GetTask(ctx context.Context, taskID string) (*BenchmarkTask, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var task BenchmarkTask
	var keyID, finished, latency sql.NullInt64
	var finishedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT task_id,api_key_id,scenario,status,COALESCE(provider_id,''),COALESCE(backend,''),COALESCE(model_key,''),COALESCE(model_id,''),COALESCE(prompt_sha256,''),prompt_length,max_tokens,COALESCE(config_json,''),run_count,started_at,finished_at,latency_ms,COALESCE(error_code,''),COALESCE(error_message,''),created_at,updated_at
		FROM benchmark_tasks WHERE task_id=?`, taskID).Scan(
		&task.TaskID, &keyID, &task.Scenario, &task.Status, &task.ProviderID, &task.Backend, &task.ModelKey,
		&task.ModelID, &task.PromptSHA256, &task.PromptLength, &task.MaxTokens, &task.ConfigJSON, &task.RunCount,
		&task.StartedAt, &finishedAt, &latency, &task.ErrorCode, &task.ErrorMessage, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if keyID.Valid {
		v := uint64(keyID.Int64)
		task.APIKeyID = &v
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	if latency.Valid {
		task.LatencyMS = latency.Int64
	}
	_ = finished // retained for compatibility with nullable scans; not used.
	return &task, nil
}

func (r *BenchmarkRepo) ListRuns(ctx context.Context, taskID string) ([]BenchmarkRun, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT run_id,task_id,run_no,request_id,status,COALESCE(provider_id,''),COALESCE(model_key,''),COALESCE(model_id,''),started_at,finished_at,latency_ms,model_load_ms,inference_latency_ms,http_status,response_bytes,input_tokens,output_tokens,total_tokens,ttft_ms,stream_event_count,reasoning_chars,content_chars,COALESCE(error_code,''),COALESCE(error_message,''),created_at,updated_at
		FROM benchmark_runs WHERE task_id=? ORDER BY run_no ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BenchmarkRun
	for rows.Next() {
		var run BenchmarkRun
		var finishedAt sql.NullTime
		var latency, load, inference, status, responseBytes sql.NullInt64
		var input, output, total, ttft sql.NullInt64
		if err := rows.Scan(&run.RunID, &run.TaskID, &run.RunNo, &run.RequestID, &run.Status,
			&run.ProviderID, &run.ModelKey, &run.ModelID, &run.StartedAt, &finishedAt,
			&latency, &load, &inference, &status, &responseBytes, &input, &output, &total, &ttft,
			&run.StreamEvents, &run.ReasoningChars, &run.ContentChars, &run.ErrorCode, &run.ErrorMessage,
			&run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		if finishedAt.Valid {
			run.FinishedAt = &finishedAt.Time
		}
		if latency.Valid { run.LatencyMS = latency.Int64 }
		if load.Valid { run.ModelLoadMS = load.Int64 }
		if inference.Valid { run.InferenceLatencyMS = inference.Int64 }
		if status.Valid { run.HTTPStatus = int(status.Int64) }
		if responseBytes.Valid { run.ResponseBytes = responseBytes.Int64 }
		if input.Valid { v := input.Int64; run.InputTokens = &v }
		if output.Valid { v := output.Int64; run.OutputTokens = &v }
		if total.Valid { v := total.Int64; run.TotalTokens = &v }
		if ttft.Valid { v := ttft.Int64; run.TTFTMS = &v }
		out = append(out, run)
	}
	return out, rows.Err()
}

// MarkInterrupted 将上次进程遗留的活动任务标记为 interrupted；不自动重放未知是否已出网的请求。
func (r *BenchmarkRepo) MarkInterrupted(ctx context.Context) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE benchmark_runs SET status='interrupted',finished_at=COALESCE(finished_at,?),error_code='gateway_restarted',error_message='Gateway restarted while run was active',updated_at=? WHERE status IN ('running','cancelling')`, now, now); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE benchmark_tasks SET status='interrupted',finished_at=COALESCE(finished_at,?),error_code='gateway_restarted',error_message='Gateway restarted while task was active',updated_at=? WHERE status IN ('running','cancelling')`, now, now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteFinishedBefore 按批次删除终态任务；benchmark_runs 通过外键级联删除。
func (r *BenchmarkRepo) DeleteFinishedBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	if batchSize < 1 || batchSize > 5000 {
		return 0, fmt.Errorf("benchmark retention batch size must be between 1 and 5000")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM benchmark_tasks
		WHERE finished_at IS NOT NULL AND finished_at < ? AND status IN ('completed','failed','cancelled','interrupted')
		ORDER BY finished_at ASC LIMIT ? FOR UPDATE`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, batchSize)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil { return 0, err }
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids { args = append(args, id) }
	res, err := tx.ExecContext(ctx, "DELETE FROM benchmark_tasks WHERE task_id IN ("+placeholders+")", args...)
	if err != nil { return 0, err }
	n, err := res.RowsAffected()
	if err != nil { return 0, err }
	if err := tx.Commit(); err != nil { return 0, err }
	return n, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" { return fallback }
	return value
}

func nullablePositiveInt64(value int64) any {
	if value < 0 { return nil }
	return value
}
