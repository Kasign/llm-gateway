package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// localInventoryAPI lists GGUF registry rows and Ollama disk tags for duplicate checks in UI.
func (s *Server) localInventoryAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	ctx := r.Context()
	ms, err := s.models.List(ctx)
	if err != nil {
		jsonError(w, 500, "database_error", err.Error())
		return
	}
	gguf := make([]map[string]interface{}, 0, len(ms))
	for _, m := range ms {
		gguf = append(gguf, map[string]interface{}{
			"model_key":  m.ModelKey,
			"repo":       m.HFRepo,
			"filename":   m.Filename,
			"status":     m.Status,
			"size_bytes": m.SizeBytes,
		})
	}
	ollamaNames := []string{}
	if s.baseURLForBackend("ollama") != "" {
		if list, err := s.InferenceList(ctx, "ollama", false); err == nil && list.Snapshot != nil {
			for _, row := range list.Snapshot.Catalog {
				if strings.TrimSpace(row.ID) != "" {
					ollamaNames = append(ollamaNames, row.ID)
				}
			}
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"gguf":          gguf,
		"ollama_names":  ollamaNames,
	})
}

func (s *Server) ollamaTagInstalled(ctx context.Context, name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return false
	}
	list, err := s.InferenceList(ctx, "ollama", false)
	if err != nil || list.Snapshot == nil {
		return false
	}
	for _, row := range list.Snapshot.Catalog {
		if strings.EqualFold(strings.TrimSpace(row.ID), name) {
			return true
		}
	}
	return false
}
