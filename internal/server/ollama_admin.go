// server（ollama_admin）：Ollama 专用管理 HTTP（列表、pull、load/unload、delete），需管理员密钥。
package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) ollamaAdminAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.baseURLForBackend("ollama") == "" {
		jsonError(w, 400, "ollama_url_missing", "未配置 OLLAMA_BASE_URL")
		return
	}
	switch r.Method {
	case http.MethodGet:
		list, err := s.InferenceList(r.Context(), "ollama", true)
		if err != nil {
			jsonError(w, 502, "ollama_error", err.Error())
			return
		}
		snap := list.Snapshot
		if snap == nil {
			jsonError(w, 502, "ollama_error", "empty upstream snapshot")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"base_url": list.BaseURL,
			"catalog":  snap.Catalog,
			"loaded":   snap.Loaded,
			"active":   snap.ActiveModel,
		})
	default:
		jsonError(w, 405, "method_not_allowed", "GET required")
	}
}

func (s *Server) ollamaPullAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, 400, "name_required", "name required")
		return
	}
	jobID, err := s.StartOllamaPull(name)
	if err != nil {
		jsonError(w, 502, "ollama_pull_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "name": name, "job_id": jobID})
}

func (s *Server) ollamaPullCancelAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		JobID string `json:"job_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		jsonError(w, 400, "invalid_body", "invalid JSON")
		return
	}
	jobID := strings.TrimSpace(in.JobID)
	if jobID == "" {
		jsonError(w, 400, "job_id_required", "job_id required")
		return
	}
	if err := s.CancelOllamaPull(jobID); err != nil {
		jsonError(w, 400, "cancel_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (s *Server) ollamaLoadAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	setInferenceLegacyDeprecation(w)
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, 400, "name_required", "name required")
		return
	}
	if _, err := s.InferenceLoad(r.Context(), "ollama", name, "", nil); err != nil {
		jsonError(w, 502, "ollama_load_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "name": name})
}

func (s *Server) ollamaUnloadAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	setInferenceLegacyDeprecation(w)
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, 400, "name_required", "name required")
		return
	}
	if _, err := s.InferenceUnload(r.Context(), "ollama", name, "", nil); err != nil {
		jsonError(w, 502, "ollama_unload_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "name": name})
}

func (s *Server) ollamaDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		jsonError(w, 400, "name_required", "name required")
		return
	}
	log := &opLogger{}
	log.add("info", "从 Ollama 磁盘删除 model="+name)
	if err := s.OllamaDeleteModel(r.Context(), name, log.add); err != nil {
		writeModelOpsResult(w, false, log, err.Error())
		return
	}
	writeModelOpsResult(w, true, log, "")
}
