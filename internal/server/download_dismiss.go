package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/whl/llm-gateway/internal/model"
)

var (
	errDismissNotFound    = errors.New("download record not found or not dismissible")
	errDismissNotTerminal = errors.New("only failed or cancelled downloads can be cleared")
)

func downloadTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "download_failed", "download_cancelled":
		return true
	default:
		return false
	}
}

func (s *Server) clearDownloadProgress(ctx context.Context, id string) {
	if s.limiter == nil || strings.TrimSpace(id) == "" {
		return
	}
	_ = s.limiter.ClearDownload(ctx, id)
}

func (s *Server) dismissHFDownloadRecord(ctx context.Context, m *model.Model) error {
	if m == nil {
		return nil
	}
	if m.Status == "downloading" {
		return errDismissNotTerminal
	}
	if !downloadTerminalStatus(m.Status) {
		return errDismissNotTerminal
	}
	if m.LocalPath != "" {
		_ = os.Remove(m.LocalPath)
	}
	s.clearDownloadProgress(ctx, m.ModelKey)
	return s.models.Delete(ctx, m.ModelKey)
}

func (s *Server) dismissOllamaDownloadRecord(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || !strings.HasPrefix(jobID, "ollama/") {
		return errDismissNotFound
	}
	v, _ := s.limiter.GetDownload(ctx, jobID)
	st := strings.TrimSpace(v["status"])
	if st == "downloading" {
		return errDismissNotTerminal
	}
	if st != "" && !downloadTerminalStatus(st) {
		return errDismissNotTerminal
	}
	s.clearDownloadProgress(ctx, jobID)
	_ = s.limiter.UntrackOllamaPullJob(ctx, jobID)
	return nil
}

// ClearFailedDownloads removes all terminal failed/cancelled HF and Ollama download rows.
func (s *Server) ClearFailedDownloads(ctx context.Context) (int, error) {
	n := 0
	ms, err := s.models.List(ctx)
	if err != nil {
		return 0, err
	}
	for i := range ms {
		m := &ms[i]
		if !downloadTerminalStatus(m.Status) {
			continue
		}
		if err := s.dismissHFDownloadRecord(ctx, m); err != nil {
			if err == errDismissNotTerminal {
				continue
			}
			return n, err
		}
		n++
	}
	for _, jobID := range s.collectOllamaPullJobIDs() {
		v, _ := s.limiter.GetDownload(ctx, jobID)
		if !downloadTerminalStatus(v["status"]) {
			continue
		}
		if err := s.dismissOllamaDownloadRecord(ctx, jobID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// DismissDownloadRecord removes one failed/cancelled task by model_key / ollama job id.
func (s *Server) DismissDownloadRecord(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errDismissNotFound
	}
	if strings.HasPrefix(key, "ollama/") {
		return s.dismissOllamaDownloadRecord(ctx, key)
	}
	m, err := s.models.GetByKey(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errDismissNotFound
		}
		return err
	}
	return s.dismissHFDownloadRecord(ctx, m)
}

func (s *Server) downloadsDismissAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		ModelKey string `json:"model_key"`
		All      bool   `json:"all_failed"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ctx := r.Context()
	if in.All {
		n, err := s.ClearFailedDownloads(ctx)
		if err != nil {
			jsonError(w, 500, "clear_failed", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cleared": n})
		return
	}
	key := strings.TrimSpace(in.ModelKey)
	if key == "" {
		jsonError(w, 400, "invalid_body", "model_key or all_failed required")
		return
	}
	if err := s.DismissDownloadRecord(ctx, key); err != nil {
		if errors.Is(err, errDismissNotFound) || errors.Is(err, errDismissNotTerminal) {
			jsonError(w, 400, "dismiss_invalid", err.Error())
			return
		}
		jsonError(w, 500, "dismiss_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "cleared": 1})
}
