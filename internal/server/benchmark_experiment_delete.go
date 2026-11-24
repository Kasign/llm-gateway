package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/store"
)

// benchmarkExperimentActionWithDeleteAPI 保留原有详情、Job 关联、状态和比较处理，
// 只为实验资源补充 DELETE；删除实验元数据不会触碰 Benchmark 原始样本。
func (s *Server) benchmarkExperimentActionWithDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.benchmarkExperimentActionAPI(w, r)
		return
	}
	if !s.requireAdmin(w, r) {
		return
	}
	const prefix = "/api/admin/research/experiments/"
	id := strings.TrimPrefix(r.URL.Path, prefix)
	if id == "" || strings.Contains(id, "/") {
		jsonError(w, http.StatusNotFound, "not_found", "research experiment not found")
		return
	}
	if s.benchmarkExperimentRepo == nil {
		jsonError(w, http.StatusInternalServerError, "repository_unavailable", "research experiment repository is unavailable")
		return
	}
	if err := s.benchmarkExperimentRepo.Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrBenchmarkExperimentActive):
			jsonError(w, http.StatusConflict, "experiment_active", "queued or running research experiments cannot be deleted")
		case errors.Is(err, sql.ErrNoRows):
			jsonError(w, http.StatusNotFound, "not_found", "research experiment not found")
		default:
			jsonError(w, http.StatusInternalServerError, "database_error", err.Error())
		}
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
