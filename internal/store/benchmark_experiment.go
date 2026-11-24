package store

import (
	"context"
	"database/sql"
	"errors"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BenchmarkExperiment 记录一次有明确单变量声明的性能研究实验。
// Job ID 仅作为关联引用，不建立级联外键，避免 Benchmark 历史清理删除实验本身。
type BenchmarkExperiment struct {
	ExperimentID string `json:"experiment_id"`
	Name string `json:"name"`
	Status string `json:"status"`
	ChangedParameter string `json:"changed_parameter"`
	BaselineValueJSON string `json:"baseline_value_json"`
	CandidateValueJSON string `json:"candidate_value_json"`
	BaselineConditionJSON string `json:"baseline_condition_json"`
	CandidateConditionJSON string `json:"candidate_condition_json"`
	BaselineJobIDsJSON string `json:"baseline_job_ids_json"`
	CandidateJobIDsJSON string `json:"candidate_job_ids_json"`
	PlannedSamples int `json:"planned_samples"`
	ReportJSON string `json:"report_json,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var ErrBenchmarkExperimentActive = errors.New("benchmark experiment is active")

// BenchmarkExperimentRepo 管理研究实验元数据；原始性能样本仍由 BenchmarkRepo 管理。
type BenchmarkExperimentRepo struct { db *sql.DB }

func NewBenchmarkExperimentRepo(db *sql.DB) *BenchmarkExperimentRepo {
	return &BenchmarkExperimentRepo{db: db}
}

func (r *BenchmarkExperimentRepo) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("benchmark experiment repository is not configured")
	}
	return nil
}

var benchmarkExperimentStatuses = map[string]struct{}{
	"draft": {}, "queued": {}, "running": {}, "partial": {},
	"completed": {}, "failed": {}, "cancelled": {}, "interrupted": {},
}

func validateExperimentJSON(label, value string, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required { return fmt.Errorf("%s is required", label) }
		return nil
	}
	if !json.Valid([]byte(value)) { return fmt.Errorf("%s must be valid JSON", label) }
	return nil
}

func validateExperiment(e BenchmarkExperiment) error {
	if strings.TrimSpace(e.ExperimentID) == "" || strings.TrimSpace(e.Name) == "" ||
		strings.TrimSpace(e.ChangedParameter) == "" {
		return fmt.Errorf("experiment identity, name and changed parameter are required")
	}
	if len(e.Name) > 160 || len(e.ChangedParameter) > 64 {
		return fmt.Errorf("experiment name or changed parameter is too long")
	}
	if _, ok := benchmarkExperimentStatuses[e.Status]; !ok {
		return fmt.Errorf("unsupported benchmark experiment status %q", e.Status)
	}
	if e.PlannedSamples < 1 || e.PlannedSamples > 20 {
		return fmt.Errorf("planned samples must be between 1 and 20")
	}
	for label, value := range map[string]string{
		"baseline value": e.BaselineValueJSON, "candidate value": e.CandidateValueJSON,
		"baseline condition": e.BaselineConditionJSON, "candidate condition": e.CandidateConditionJSON,
		"baseline job ids": e.BaselineJobIDsJSON, "candidate job ids": e.CandidateJobIDsJSON,
	} {
		if err := validateExperimentJSON(label, value, true); err != nil { return err }
	}
	var baselineIDs, candidateIDs []string
	if err := json.Unmarshal([]byte(e.BaselineJobIDsJSON), &baselineIDs); err != nil || baselineIDs == nil {
		return fmt.Errorf("baseline job ids must be a JSON array")
	}
	if err := json.Unmarshal([]byte(e.CandidateJobIDsJSON), &candidateIDs); err != nil || candidateIDs == nil {
		return fmt.Errorf("candidate job ids must be a JSON array")
	}
	if err := validateExperimentJSON("report", e.ReportJSON, false); err != nil { return err }
	return nil
}

func (r *BenchmarkExperimentRepo) Create(ctx context.Context, e BenchmarkExperiment) error {
	if err := r.ready(); err != nil { return err }
	if e.Status == "" { e.Status = "draft" }
	if e.PlannedSamples == 0 { e.PlannedSamples = 10 }
	if e.BaselineJobIDsJSON == "" { e.BaselineJobIDsJSON = "[]" }
	if e.CandidateJobIDsJSON == "" { e.CandidateJobIDsJSON = "[]" }
	if e.BaselineValueJSON == "" { e.BaselineValueJSON = "null" }
	if e.CandidateValueJSON == "" { e.CandidateValueJSON = "null" }
	if e.BaselineConditionJSON == "" { e.BaselineConditionJSON = "{}" }
	if e.CandidateConditionJSON == "" { e.CandidateConditionJSON = "{}" }
	if err := validateExperiment(e); err != nil { return err }
	now := time.Now()
	if e.CreatedAt.IsZero() { e.CreatedAt = now }
	if e.UpdatedAt.IsZero() { e.UpdatedAt = now }
	_, err := r.db.ExecContext(ctx, `INSERT INTO benchmark_experiments
		(experiment_id,name,status,changed_parameter,baseline_value_json,candidate_value_json,
		 baseline_condition_json,candidate_condition_json,baseline_job_ids_json,candidate_job_ids_json,
		 planned_samples,report_json,error_code,error_message,started_at,finished_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ExperimentID,e.Name,e.Status,e.ChangedParameter,e.BaselineValueJSON,e.CandidateValueJSON,
		e.BaselineConditionJSON,e.CandidateConditionJSON,e.BaselineJobIDsJSON,e.CandidateJobIDsJSON,
		e.PlannedSamples,nullIfEmpty(e.ReportJSON),nullIfEmpty(e.ErrorCode),nullIfEmpty(e.ErrorMessage),
		e.StartedAt,e.FinishedAt,e.CreatedAt,e.UpdatedAt)
	return err
}

func (r *BenchmarkExperimentRepo) Get(ctx context.Context, id string) (*BenchmarkExperiment, error) {
	if err := r.ready(); err != nil { return nil, err }
	var e BenchmarkExperiment
	var report, code, message sql.NullString
	var started, finished sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT experiment_id,name,status,changed_parameter,
		baseline_value_json,candidate_value_json,baseline_condition_json,candidate_condition_json,
		baseline_job_ids_json,candidate_job_ids_json,planned_samples,report_json,error_code,error_message,
		started_at,finished_at,created_at,updated_at
		FROM benchmark_experiments WHERE experiment_id=?`, id).Scan(
		&e.ExperimentID,&e.Name,&e.Status,&e.ChangedParameter,&e.BaselineValueJSON,&e.CandidateValueJSON,
		&e.BaselineConditionJSON,&e.CandidateConditionJSON,&e.BaselineJobIDsJSON,&e.CandidateJobIDsJSON,
		&e.PlannedSamples,&report,&code,&message,&started,&finished,&e.CreatedAt,&e.UpdatedAt)
	if err != nil { return nil, err }
	if report.Valid { e.ReportJSON = report.String }
	if code.Valid { e.ErrorCode = code.String }
	if message.Valid { e.ErrorMessage = message.String }
	if started.Valid { e.StartedAt = &started.Time }
	if finished.Valid { e.FinishedAt = &finished.Time }
	return &e,nil
}

func (r *BenchmarkExperimentRepo) List(ctx context.Context, status string, limit, offset int) ([]BenchmarkExperiment,int,error) {
	if err := r.ready(); err != nil { return nil,0,err }
	if limit < 1 || limit > 100 || offset < 0 { return nil,0,fmt.Errorf("invalid benchmark experiment pagination") }
	if status != "" { if _,ok:=benchmarkExperimentStatuses[status]; !ok { return nil,0,fmt.Errorf("unsupported benchmark experiment status %q",status) } }
	where,args := "",[]any{}
	if status != "" { where=" WHERE status=?"; args=append(args,status) }
	var total int
	if err:=r.db.QueryRowContext(ctx,"SELECT COUNT(*) FROM benchmark_experiments"+where,args...).Scan(&total); err!=nil { return nil,0,err }
	queryArgs:=append(append([]any(nil),args...),limit,offset)
	rows,err:=r.db.QueryContext(ctx,`SELECT experiment_id,name,status,changed_parameter,
		baseline_value_json,candidate_value_json,baseline_condition_json,candidate_condition_json,
		baseline_job_ids_json,candidate_job_ids_json,planned_samples,report_json,error_code,error_message,
		started_at,finished_at,created_at,updated_at FROM benchmark_experiments`+where+
		` ORDER BY updated_at DESC,experiment_id DESC LIMIT ? OFFSET ?`,queryArgs...)
	if err!=nil{return nil,0,err}
	defer rows.Close()
	out:=make([]BenchmarkExperiment,0,limit)
	for rows.Next(){
		var e BenchmarkExperiment
		var report,code,message sql.NullString
		var started,finished sql.NullTime
		if err:=rows.Scan(&e.ExperimentID,&e.Name,&e.Status,&e.ChangedParameter,
			&e.BaselineValueJSON,&e.CandidateValueJSON,&e.BaselineConditionJSON,&e.CandidateConditionJSON,
			&e.BaselineJobIDsJSON,&e.CandidateJobIDsJSON,&e.PlannedSamples,&report,&code,&message,
			&started,&finished,&e.CreatedAt,&e.UpdatedAt);err!=nil{return nil,0,err}
		if report.Valid{e.ReportJSON=report.String};if code.Valid{e.ErrorCode=code.String};if message.Valid{e.ErrorMessage=message.String}
		if started.Valid{e.StartedAt=&started.Time};if finished.Valid{e.FinishedAt=&finished.Time}
		out=append(out,e)
	}
	if err:=rows.Err();err!=nil{return nil,0,err}
	return out,total,nil
}

// AddJob 在事务中追加一条已创建的 Benchmark Job 关联；重复回调幂等，不重复计数。
func (r *BenchmarkExperimentRepo) AddJob(ctx context.Context, id, group, jobID string) error {
	if err:=r.ready();err!=nil{return err}
	if strings.TrimSpace(id)==""||strings.TrimSpace(jobID)==""||(group!="baseline"&&group!="candidate"){
		return fmt.Errorf("invalid experiment job association")
	}
	tx,err:=r.db.BeginTx(ctx,nil);if err!=nil{return err};defer tx.Rollback()
	var raw string
	column:="baseline_job_ids_json"
	if group=="candidate"{column="candidate_job_ids_json"}
	if err:=tx.QueryRowContext(ctx,"SELECT "+column+" FROM benchmark_experiments WHERE experiment_id=? FOR UPDATE",id).Scan(&raw);err!=nil{return err}
	var ids []string
	if err:=json.Unmarshal([]byte(raw),&ids);err!=nil{return fmt.Errorf("stored %s job ids are invalid: %w",group,err)}
	for _,existing:=range ids{if existing==jobID{return tx.Commit()}}
	ids=append(ids,jobID)
	encoded,err:=json.Marshal(ids);if err!=nil{return err}
	if _,err:=tx.ExecContext(ctx,"UPDATE benchmark_experiments SET "+column+"=?,updated_at=? WHERE experiment_id=?",string(encoded),time.Now(),id);err!=nil{return err}
	return tx.Commit()
}

// UpdateState 更新实验状态、报告和错误摘要；不触碰原始 Benchmark Task/Run。
func (r *BenchmarkExperimentRepo) UpdateState(ctx context.Context,id,status,reportJSON,errorCode,errorMessage string,startedAt,finishedAt *time.Time) error {
	if err:=r.ready();err!=nil{return err}
	if _,ok:=benchmarkExperimentStatuses[status];!ok{return fmt.Errorf("unsupported benchmark experiment status %q",status)}
	if err:=validateExperimentJSON("report",reportJSON,false);err!=nil{return err}
	res,err:=r.db.ExecContext(ctx,`UPDATE benchmark_experiments SET status=?,report_json=?,error_code=?,error_message=?,
		started_at=COALESCE(started_at,?),finished_at=?,updated_at=? WHERE experiment_id=?`,
		status,nullIfEmpty(reportJSON),nullIfEmpty(errorCode),nullIfEmpty(errorMessage),startedAt,finishedAt,time.Now(),id)
	if err!=nil{return err}
	n,err:=res.RowsAffected();if err!=nil{return err};if n==0{
		var exists int
		if err:=r.db.QueryRowContext(ctx,"SELECT COUNT(*) FROM benchmark_experiments WHERE experiment_id=?",id).Scan(&exists);err!=nil{return err}
		if exists==0{return sql.ErrNoRows}
	}
	return nil
}

// Delete 删除实验元数据，不级联删除 Benchmark Task/Run、请求审计或原始样本。
// queued / running 实验受保护；状态判断与删除在同一 SQL 语句中完成，避免并发启动时误删。
func (r *BenchmarkExperimentRepo) Delete(ctx context.Context, id string) error {
	if err := r.ready(); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM benchmark_experiments
		WHERE experiment_id=? AND status NOT IN ('queued','running')`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var status string
	if err := r.db.QueryRowContext(ctx, `SELECT status FROM benchmark_experiments WHERE experiment_id=?`, id).Scan(&status); err != nil {
		return err
	}
	if status == "queued" || status == "running" {
		return ErrBenchmarkExperimentActive
	}
	return sql.ErrNoRows
}

// MarkRunningInterrupted 在 Gateway 启动恢复时结束上次进程遗留的运行中实验。
// 实验执行由浏览器驱动，进程重启后无法安全续跑；保留已关联 Job 与报告，不自动重放。
func (r *BenchmarkExperimentRepo) MarkRunningInterrupted(ctx context.Context) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	now := time.Now()
	res, err := r.db.ExecContext(ctx, `UPDATE benchmark_experiments
		SET status='interrupted',
			error_code='gateway_restarted',
			error_message='Gateway restarted while experiment was active',
			finished_at=COALESCE(finished_at,?),
			updated_at=?
		WHERE status='running'`, now, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
