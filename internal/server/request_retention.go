package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
)

const requestRetentionMaxBatchesPerRun = 10

// requestRetentionRuntimeStatus 是当前进程内的保留清理计数；不会持久化，也不包含数据库原始错误。
type requestRetentionRuntimeStatus struct {
	LastRunStatus      string `json:"last_run_status"`
	RunCount           int64  `json:"run_count"`
	SuccessCount       int64  `json:"success_count"`
	FailureCount       int64  `json:"failure_count"`
	LastRunStartedAt   string `json:"last_run_started_at,omitempty"`
	LastRunFinishedAt  string `json:"last_run_finished_at,omitempty"`
	LastRunDurationMS  int64  `json:"last_run_duration_ms"`
	LastRunDeletedRows int64  `json:"last_run_deleted_rows"`
	TotalDeletedRows   int64  `json:"total_deleted_rows"`
	LastFailureCode    string `json:"last_failure_code,omitempty"`
	LastFailureAt      string `json:"last_failure_at,omitempty"`
}

// requestRetentionStatusResponse 返回静态保留配置与当前进程的最近执行快照。
type requestRetentionStatusResponse struct {
	Enabled          bool                          `json:"enabled"`
	RetentionDays    int                           `json:"retention_days"`
	BatchSize        int                           `json:"batch_size"`
	IntervalSeconds  int                           `json:"interval_seconds"`
	MaxBatchesPerRun int                           `json:"max_batches_per_run"`
	Runtime          requestRetentionRuntimeStatus `json:"runtime"`
}

// startRequestRetention 只在显式启用保留期限时启动后台任务；默认配置绝不删除历史请求。
func (s *Server) startRequestRetention() {
	if s == nil || s.cfg == nil {
		return
	}
	if s.cfg.Logging.RetentionDays <= 0 {
		s.updateRequestRetentionStatus(func(status *requestRetentionRuntimeStatus) {
			status.LastRunStatus = "disabled"
		})
		return
	}
	if s.logs == nil {
		s.updateRequestRetentionStatus(func(status *requestRetentionRuntimeStatus) {
			status.LastRunStatus = "unavailable"
		})
		applog.Printf("request retention is configured but unavailable: request log repository is not configured")
		return
	}
	s.updateRequestRetentionStatus(func(status *requestRetentionRuntimeStatus) {
		if status.LastRunStatus == "" {
			status.LastRunStatus = "waiting"
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	s.requestRetentionCancel = cancel
	s.requestRetentionWG.Add(1)
	interval := time.Duration(s.cfg.Logging.RetentionIntervalSeconds) * time.Second
	applog.Infof("request retention enabled: days=%d batch=%d interval=%s", s.cfg.Logging.RetentionDays, s.cfg.Logging.RetentionBatchSize, interval)
	go func() {
		defer s.requestRetentionWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.cleanupExpiredRequests(ctx, now)
			}
		}
	}()
}

func (s *Server) updateRequestRetentionStatus(update func(*requestRetentionRuntimeStatus)) {
	if s == nil || update == nil {
		return
	}
	s.requestRetentionStatusMu.Lock()
	update(&s.requestRetentionStatus)
	s.requestRetentionStatusMu.Unlock()
}

func (s *Server) beginRequestRetentionRun(started time.Time) {
	s.updateRequestRetentionStatus(func(status *requestRetentionRuntimeStatus) {
		status.LastRunStatus = "running"
		status.RunCount++
		status.LastRunStartedAt = started.UTC().Format(time.RFC3339Nano)
		status.LastRunFinishedAt = ""
		status.LastRunDurationMS = 0
		status.LastRunDeletedRows = 0
	})
}

// finishRequestRetentionRun 发布聚合结果；错误只映射为稳定代码，原始数据库异常只进入服务端日志。
func (s *Server) finishRequestRetentionRun(started, finished time.Time, deleted int64, runErr error) {
	s.updateRequestRetentionStatus(func(status *requestRetentionRuntimeStatus) {
		status.LastRunFinishedAt = finished.UTC().Format(time.RFC3339Nano)
		status.LastRunDurationMS = finished.Sub(started).Milliseconds()
		status.LastRunDeletedRows = deleted
		status.TotalDeletedRows += deleted
		switch {
		case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
			status.LastRunStatus = "cancelled"
		case runErr != nil:
			status.LastRunStatus = "failed"
			status.FailureCount++
			status.LastFailureCode = "cleanup_failed"
			status.LastFailureAt = finished.UTC().Format(time.RFC3339Nano)
		default:
			status.LastRunStatus = "success"
			status.SuccessCount++
		}
	})
}

func (s *Server) requestRetentionRuntimeSnapshot() requestRetentionRuntimeStatus {
	if s == nil {
		return requestRetentionRuntimeStatus{LastRunStatus: "unavailable"}
	}
	s.requestRetentionStatusMu.RLock()
	defer s.requestRetentionStatusMu.RUnlock()
	return s.requestRetentionStatus
}

func (s *Server) requestRetentionStatusSnapshot() requestRetentionStatusResponse {
	response := requestRetentionStatusResponse{MaxBatchesPerRun: requestRetentionMaxBatchesPerRun}
	if s == nil || s.cfg == nil {
		response.Runtime.LastRunStatus = "unavailable"
		return response
	}
	cfg := s.cfg.Logging
	response.Enabled = cfg.RetentionDays > 0
	response.RetentionDays = cfg.RetentionDays
	response.BatchSize = cfg.RetentionBatchSize
	response.IntervalSeconds = cfg.RetentionIntervalSeconds
	response.Runtime = s.requestRetentionRuntimeSnapshot()
	switch {
	case !response.Enabled:
		response.Runtime.LastRunStatus = "disabled"
	case s.logs == nil:
		response.Runtime.LastRunStatus = "unavailable"
	case response.Runtime.LastRunStatus == "":
		response.Runtime.LastRunStatus = "waiting"
	}
	return response
}

// requestRetentionStatusAPI 提供只读运行状态。指标保存在当前进程内，重启后计数归零。
func (s *Server) requestRetentionStatusAPI(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg == nil {
		jsonError(w, http.StatusServiceUnavailable, "retention_unavailable", "request retention status unavailable")
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]interface{}{"data": s.requestRetentionStatusSnapshot()})
}

// cleanupExpiredRequests 每轮最多清理十个小批次，避免历史积压导致长事务或持续占用数据库。
func (s *Server) cleanupExpiredRequests(ctx context.Context, now time.Time) {
	started := time.Now()
	s.beginRequestRetentionRun(started)
	cutoff := now.Add(-time.Duration(s.cfg.Logging.RetentionDays) * 24 * time.Hour)
	batchSize := s.cfg.Logging.RetentionBatchSize
	var total int64
	for i := 0; i < requestRetentionMaxBatchesPerRun; i++ {
		if ctx.Err() != nil {
			s.finishRequestRetentionRun(started, time.Now(), total, ctx.Err())
			return
		}
		deleted, err := s.logs.DeleteFinishedRequestsBefore(ctx, cutoff, batchSize)
		if err != nil {
			if ctx.Err() != nil {
				s.finishRequestRetentionRun(started, time.Now(), total, ctx.Err())
				return
			}
			applog.Printf("request retention cleanup failed: %v", err)
			s.finishRequestRetentionRun(started, time.Now(), total, err)
			return
		}
		if deleted == 0 {
			break
		}
		total += deleted
	}
	finished := time.Now()
	s.finishRequestRetentionRun(started, finished, total, nil)
	if total > 0 {
		applog.Infof("request retention cleanup completed: deleted=%d cutoff=%s duration=%s", total, cutoff.UTC().Format(time.RFC3339), finished.Sub(started))
	}
}
