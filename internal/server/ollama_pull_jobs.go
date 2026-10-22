package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
)

func ollamaPullJobID(name string) string {
	return "ollama/" + strings.NewReplacer(":", "__", "/", "_").Replace(strings.TrimSpace(name))
}

const (
	ollamaPullDoneVisibleSec   = 30 * 60
	ollamaPullFailedVisibleSec = 6 * 60 * 60
)

// ollamaPullJobListed decides whether a job row should appear in /api/downloads.
func ollamaPullJobListed(status string, updatedAt, now int64) bool {
	status = strings.TrimSpace(status)
	if status == "" {
		return false
	}
	if status == "downloading" {
		return true
	}
	if updatedAt <= 0 {
		updatedAt = now
	}
	age := now - updatedAt
	switch status {
	case "ready":
		return age <= ollamaPullDoneVisibleSec
	case "download_failed", "download_cancelled":
		return age <= ollamaPullFailedVisibleSec
	default:
		return age <= ollamaPullDoneVisibleSec
	}
}

func (s *Server) collectOllamaPullJobIDs() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || !strings.HasPrefix(id, "ollama/") {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if jobs, err := s.limiter.ListOllamaPullJobs(context.Background()); err == nil {
		for _, id := range jobs {
			add(id)
		}
	}
	s.downloadMu.Lock()
	for id := range s.downloadCancel {
		add(id)
	}
	s.downloadMu.Unlock()
	return out
}

func (s *Server) setOllamaPullProgress(ctx context.Context, jobID, name, status string, done, total int64, errMsg string) {
	vals := map[string]interface{}{
		"kind":       "ollama",
		"name":       name,
		"status":     status,
		"done":       done,
		"total":      total,
		"updated_at": time.Now().Unix(),
	}
	if errMsg != "" {
		vals["error"] = errMsg
	}
	_ = s.limiter.SetDownload(ctx, jobID, vals)
	if errMsg == "" {
		_ = s.limiter.DeleteDownloadFields(ctx, jobID, "error")
	}
}

// StartOllamaPull enqueues an async pull; progress appears in /api/downloads.
func (s *Server) StartOllamaPull(name string) (jobID string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name required")
	}
	if s.baseURLForBackend("ollama") == "" {
		return "", fmt.Errorf("未配置 OLLAMA_BASE_URL")
	}
	if s.ollamaTagInstalled(context.Background(), name) {
		return "", fmt.Errorf("本地 Ollama 已存在该模型：%s", name)
	}
	jobID = ollamaPullJobID(name)
	s.downloadMu.Lock()
	if _, running := s.downloadCancel[jobID]; running {
		s.downloadMu.Unlock()
		return "", fmt.Errorf("该模型已在下载中")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.Download.Timeout)*time.Second)
	s.downloadCancel[jobID] = cancel
	s.downloadMu.Unlock()

	_ = s.limiter.TrackOllamaPullJob(context.Background(), jobID)
	s.setOllamaPullProgress(context.Background(), jobID, name, "downloading", 0, 0, "")

	go s.runOllamaPull(ctx, jobID, name)
	return jobID, nil
}

func (s *Server) runOllamaPull(ctx context.Context, jobID, name string) {
	defer func() {
		s.downloadMu.Lock()
		delete(s.downloadCancel, jobID)
		s.downloadMu.Unlock()
	}()

	client, _, err := s.adminClientForBackend("ollama")
	if err != nil {
		s.setOllamaPullProgress(context.Background(), jobID, name, "download_failed", 0, 0, err.Error())
		return
	}
	applog.Printf("ollama pull started: job=%s name=%s", jobID, name)
	var lastDone, lastTotal int64
	err = client.OllamaPull(ctx, name, func(done, total int64, status string) {
		if done > 0 {
			lastDone = done
		}
		if total > 0 {
			lastTotal = total
		}
		s.setOllamaPullProgress(context.Background(), jobID, name, "downloading", lastDone, lastTotal, "")
	})
	if err != nil {
		st := "download_failed"
		if ctx.Err() != nil {
			st = "download_cancelled"
		}
		s.setOllamaPullProgress(context.Background(), jobID, name, st, lastDone, lastTotal, err.Error())
		applog.Printf("ollama pull failed: job=%s err=%v", jobID, err)
		return
	}
	s.setOllamaPullProgress(context.Background(), jobID, name, "ready", lastTotal, lastTotal, "")
	s.invalidateAdminRuntimeCaches()
	applog.Printf("ollama pull finished: job=%s name=%s", jobID, name)
}

// CancelOllamaPull stops an in-flight Ollama pull job.
func (s *Server) CancelOllamaPull(jobID string) error {
	jobID = strings.TrimSpace(jobID)
	s.downloadMu.Lock()
	cancel, ok := s.downloadCancel[jobID]
	s.downloadMu.Unlock()
	if !ok {
		return fmt.Errorf("download not running")
	}
	cancel()
	s.setOllamaPullProgress(context.Background(), jobID, "", "download_cancelled", 0, 0, "")
	return nil
}
