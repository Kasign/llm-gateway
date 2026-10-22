package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/inferenceparams"
)

func (s *Server) inferenceParamsStore() *inferenceparams.Store {
	return inferenceparams.NewStore(s.settings, s.cfg)
}

func (s *Server) inferenceParamsSchemaAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"fields":            inferenceparams.Schema(),
		"sections":          inferenceparams.SectionTitles(),
		"section_descriptions": inferenceparams.SectionDescriptions(),
	})
}

func (s *Server) inferenceParamsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	backend := normalizeInferenceParamsBackend(r.URL.Query().Get("backend"))
	switch r.Method {
	case http.MethodGet:
		p, pol, err := s.inferenceParamsStore().Load(r.Context(), backend)
		if err != nil {
			jsonError(w, 500, "database_error", err.Error())
			return
		}
		effective, _, _ := s.inferenceParamsStore().LoadEffective(r.Context(), backend)
		llamaD, ollamaD, factoryPol := inferenceparams.DefaultsFromConfig(s.cfg)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"backend": backend, "params": p, "policy": pol, "effective": effective,
			"defaults": map[string]interface{}{
				"llamacpp": llamaD,
				"ollama":   ollamaD,
			},
			"default_policy": map[string]interface{}{
				"llamacpp": factoryPol,
				"ollama":   factoryPol,
			},
		})
	case http.MethodPut:
		var in struct {
			Backend string                `json:"backend"`
			Params  inferenceparams.Params `json:"params"`
			Policy  inferenceparams.Policy `json:"policy"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			jsonError(w, 400, "invalid_request", "invalid JSON")
			return
		}
		be := normalizeInferenceParamsBackend(in.Backend)
		if err := s.inferenceParamsStore().Save(r.Context(), be, in.Params); err != nil {
			jsonError(w, 500, "database_error", err.Error())
			return
		}
		if in.Policy.Mode != "" {
			if err := s.inferenceParamsStore().SavePolicy(r.Context(), be, in.Policy); err != nil {
				jsonError(w, 500, "database_error", err.Error())
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	default:
		jsonError(w, 405, "method_not_allowed", "GET or PUT required")
	}
}

func (s *Server) inferenceParamsResetAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Backend string `json:"backend"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	be := normalizeInferenceParamsBackend(in.Backend)
	p, err := s.inferenceParamsStore().Reset(r.Context(), be)
	if err != nil {
		jsonError(w, 500, "database_error", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"params": p})
}

func normalizeInferenceParamsBackend(b string) string {
	b = strings.ToLower(strings.TrimSpace(b))
	if b == "ollama" {
		return "ollama"
	}
	return "llamacpp"
}
