package server

import "net/http"

// auditMetricsAPI 为管理台提供审计队列快照。该接口只读取内存指标，不访问数据库。
func (s *Server) auditMetricsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if s.audit == nil {
		jsonError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit metrics unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]interface{}{"data": s.audit.Stats()})
}
