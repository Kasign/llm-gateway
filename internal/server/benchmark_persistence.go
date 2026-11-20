package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/store"
)

// persistenceRecords 在 job 锁内生成 Task/Run 快照；正文不进入 Benchmark 表。
func (j *benchmarkJob) persistenceRecords() (store.BenchmarkTask, store.BenchmarkRun) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	task := j.taskRecord
	run := j.runRecord
	task.Status = j.Status
	task.ModelKey = j.ModelKey
	task.ModelID = j.LlamaModel
	task.FinishedAt = j.FinishedAt
	task.LatencyMS = j.LatencyMS
	task.ErrorCode = j.ErrorCode
	task.ErrorMessage = ""
	if j.ErrorCode != "" {
		task.ErrorMessage = "Benchmark execution failed: " + j.ErrorCode
	}
	run.Status = j.Status
	run.ModelKey = j.ModelKey
	run.ModelID = j.LlamaModel
	run.FinishedAt = j.FinishedAt
	run.LatencyMS = j.LatencyMS
	run.ModelLoadMS = j.ModelLoadMS
	run.ModelLoadStatus = j.ModelLoadStatus
	run.InferenceLatencyMS = j.InferenceLatencyMS
	run.InferenceLatencyStatus = j.InferenceLatencyStatus
	run.TTFTStatus = j.TTFTStatus
	run.ToolCallDecision = j.ToolCallDecision
	run.ToolCallEventCount = j.ToolCallEventCount
	if len(j.ModelRoundDurationsMS) > 0 {
		if encoded, err := json.Marshal(j.ModelRoundDurationsMS); err == nil { run.ModelRoundDurationsJSON = string(encoded) }
	}
	run.HTTPStatus = j.HTTPStatus
	run.ResponseBytes = j.ResponseBytes
	if j.InputTokens > 0 { v := j.InputTokens; run.InputTokens = &v }
	if j.OutputTokens > 0 { v := j.OutputTokens; run.OutputTokens = &v }
	if j.TotalTokens > 0 { v := j.TotalTokens; run.TotalTokens = &v }
	run.TTFTMS = j.TTFTMS
	run.StreamEvents = j.StreamEvents
	run.ReasoningChars = j.ReasoningChars
	run.ContentChars = j.ContentChars
	run.ErrorCode = j.ErrorCode
	run.ErrorMessage = task.ErrorMessage
	return task, run
}

func (s *Server) persistBenchmarkJob(job *benchmarkJob) {
	if s == nil || s.benchmarkRepo == nil || job == nil {
		return
	}
	task, run := job.persistenceRecords()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.benchmarkRepo.UpdateTaskAndRun(ctx, task, run); err != nil {
		applog.Printf("[benchmark:%s] task persistence update failed: %v", task.TaskID, err)
	}
}

// recoverBenchmarkTasks 在启动时将上次进程遗留的活动任务标记为 interrupted，不自动重放模型请求。
func (s *Server) recoverBenchmarkTasks(ctx context.Context) {
	if s == nil || s.benchmarkRepo == nil {
		return
	}
	count, err := s.benchmarkRepo.MarkInterrupted(ctx)
	if err != nil {
		applog.Printf("benchmark recovery failed: %v", err)
		return
	}
	if count > 0 {
		applog.Infof("benchmark recovery: marked %d task(s) interrupted", count)
	}
}

// benchmarkGetFromStore 在内存任务被清理或 Gateway 重启后，从持久化 Task/Run 恢复摘要。
func (s *Server) benchmarkGetFromStore(w http.ResponseWriter, r *http.Request, id string) {
	if s.benchmarkRepo == nil {
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "benchmark task storage is unavailable")
		return
	}
	task, err := s.benchmarkRepo.GetTask(r.Context(), id)
	if err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, http.StatusNotFound, "not_found", "benchmark job not found")
		} else {
			jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "benchmark task storage is unavailable")
		}
		return
	}
	runs, err := s.benchmarkRepo.ListRuns(r.Context(), id)
	if err != nil || len(runs) == 0 {
		jsonError(w, http.StatusServiceUnavailable, "benchmark_store_unavailable", "benchmark run details are unavailable")
		return
	}
	run := runs[len(runs)-1]
	conditionSHA256, conditionStatus, conditionMissing := benchmarkConditionFromConfigJSON(task.ConfigJSON)
	snap := benchmarkJobSnapshot{
		ID: task.TaskID, Scenario: task.Scenario, AcceptanceCaseID: benchmarkAcceptanceCaseIDFromConfigJSON(task.ConfigJSON), Status: task.Status, ModelKey: task.ModelKey, LlamaModel: run.ModelID,
		ConditionSHA256: conditionSHA256, ConditionStatus: conditionStatus, ConditionMissing: conditionMissing,
		ConditionSnapshot: benchmarkConditionSnapshotFromConfigJSON(task.ConfigJSON),
		PromptLen: int(task.PromptLength), MaxTokens: task.MaxTokens, StartedAt: task.StartedAt,
		FinishedAt: task.FinishedAt, LatencyMS: task.LatencyMS, HTTPStatus: run.HTTPStatus,
		ResponseBytes: run.ResponseBytes, ResponseTruncated: task.ErrorCode == "benchmark_response_too_large",
		ModelLoadMS: run.ModelLoadMS, ModelLoadStatus: run.ModelLoadStatus,
		InferenceLatencyMS: run.InferenceLatencyMS, InferenceLatencyStatus: run.InferenceLatencyStatus, TTFTStatus: run.TTFTStatus,
		InputTokens: valueOrZero(run.InputTokens),
		OutputTokens: valueOrZero(run.OutputTokens), TotalTokens: valueOrZero(run.TotalTokens),
		Error: task.ErrorMessage, ErrorCode: task.ErrorCode, RequestID: run.RequestID,
		TTFTMS: run.TTFTMS, StreamEvents: run.StreamEvents, ReasoningChars: run.ReasoningChars,
		ContentChars: run.ContentChars,
		ModelRoundDurationsMS: decodeBenchmarkRoundDurations(run.ModelRoundDurationsJSON),
		ToolCallDecision: run.ToolCallDecision, ToolCallEventCount: run.ToolCallEventCount,
		Stream: !isBenchmarkToolScenario(task.Scenario),
	}
	events := []store.StreamEvent{}
	toolEvents := []store.ToolCallEvent{}
	toolEventsAvailable := false
	if s.logs != nil {
		events, _ = s.logs.ListStreamEvents(r.Context(), run.RequestID, 2000)
		var toolEventsErr error
		toolEvents, toolEventsErr = s.logs.ListToolCallEvents(r.Context(), run.RequestID, 9)
		toolEventsAvailable = toolEventsErr == nil
		if toolEventsErr != nil { toolEvents = nil }
	}
	toolMetrics := buildBenchmarkToolMetrics(snap, toolEvents, toolEventsAvailable)
	out := map[string]interface{}{
		"job": snap, "stream_events": events, "tool_call_events": toolEvents, "tool_metrics": toolMetrics, "request_id": snap.RequestID,
		"request_log": "/api/requests/" + snap.RequestID, "payload_persisted": false,
	}
	if snap.AcceptanceCaseID != "" {
		out["acceptance"] = evaluateBenchmarkAcceptanceWithEvents(snap.AcceptanceCaseID, snap, toolMetrics, toolEvents)
	}
	if s.cfg != nil && s.cfg.Logging.StorePayload && s.logs != nil {
		if payload, err := s.logs.GetPayload(r.Context(), run.RequestID); err == nil {
			if response := payload["response_body"]; response != "" {
				raw := []byte(response)
				snap.Response = response
				snap.Answer = extractChatAnswer(raw)
				if logutil.LooksLikeSSE(raw) {
					content, reasoning := logutil.AggregateChatStream(raw)
					snap.ContentText, snap.ReasoningText = content, reasoning
					snap.ContentChars, snap.ReasoningChars = len(content), len(reasoning)
				}
				out["job"] = snap
				out["payload_persisted"] = true
			}
		}
	}
	if task.Status != "running" && task.Status != "cancelling" && s.logs != nil {
		if raw, err := s.logs.GetRequestAnalysis(r.Context(), run.RequestID); err == nil {
			if analysis, err := decodeRequestAnalysis(raw); err == nil && analysis != nil {
				out["analysis"] = analysis
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// benchmarkConditionFromConfigJSON 从 Task 配置快照恢复比较元数据；兼容早期没有这些字段的历史记录。
func benchmarkConditionFromConfigJSON(raw string) (string, string, []string) {
	var condition struct {
		SHA256  string   `json:"condition_sha256"`
		Status  string   `json:"condition_status"`
		Missing []string `json:"condition_missing"`
	}
	if err := json.Unmarshal([]byte(raw), &condition); err != nil || condition.Status == "" {
		return "", "unknown", nil
	}
	return condition.SHA256, condition.Status, append([]string(nil), condition.Missing...)
}

func valueOrZero(value *int64) int64 {
	if value == nil { return 0 }
	return *value
}


// benchmarkConditionSnapshotFromConfigJSON 恢复创建任务时保存的完整条件快照；无效或旧格式记录不伪造字段。
func benchmarkConditionSnapshotFromConfigJSON(raw string) map[string]interface{} {
	var snapshot map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil || len(snapshot) == 0 {
		return nil
	}
	return snapshot
}
