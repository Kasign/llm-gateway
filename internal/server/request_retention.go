package server

import (
	"context"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
)

// startRequestRetention 只在显式启用保留期限时启动后台任务；默认配置绝不删除历史请求。
func (s *Server) startRequestRetention() {
	if s == nil || s.cfg == nil || s.logs == nil || s.cfg.Logging.RetentionDays <= 0 {
		return
	}
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

// cleanupExpiredRequests 每轮最多清理十个小批次，避免历史积压导致长事务或持续占用数据库。
func (s *Server) cleanupExpiredRequests(ctx context.Context, now time.Time) {
	cutoff := now.Add(-time.Duration(s.cfg.Logging.RetentionDays) * 24 * time.Hour)
	batchSize := s.cfg.Logging.RetentionBatchSize
	const maxBatchesPerRun = 10
	var total int64
	started := time.Now()
	for i := 0; i < maxBatchesPerRun; i++ {
		if ctx.Err() != nil {
			return
		}
		deleted, err := s.logs.DeleteFinishedRequestsBefore(ctx, cutoff, batchSize)
		if err != nil {
			if ctx.Err() == nil {
				applog.Printf("request retention cleanup failed: %v", err)
			}
			return
		}
		if deleted == 0 {
			break
		}
		total += deleted
	}
	if total > 0 {
		applog.Infof("request retention cleanup completed: deleted=%d cutoff=%s duration=%s", total, cutoff.UTC().Format(time.RFC3339), time.Since(started))
	}
}
