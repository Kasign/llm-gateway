package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/settings"
)

func (s *Server) backendRuntimeProfilesAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.settings == nil {
		jsonError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings storage is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := s.settings.ListBackendRuntimeProfiles(r.Context(), r.URL.Query().Get("backend"), r.URL.Query().Get("model_id"))
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "settings_read_failed", "failed to read backend runtime profiles")
			return
		}
		if items == nil {
			items = []settings.BackendRuntimeProfile{}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
	case http.MethodPost, http.MethodPut:
		var profile settings.BackendRuntimeProfile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&profile); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid_request", "invalid runtime profile JSON")
			return
		}
		if r.Method == http.MethodPost {
			profile.ID = newID()
		} else if strings.TrimSpace(profile.ID) == "" {
			jsonError(w, http.StatusBadRequest, "invalid_request", "profile id is required for update")
			return
		}
		saved, err := s.settings.SaveBackendRuntimeProfile(r.Context(), profile)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "invalid_runtime_profile", err.Error())
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"profile": saved})
	default:
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET, POST or PUT required")
	}
}

func (s *Server) backendRuntimeProfileActionAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.settings == nil {
		jsonError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings storage is unavailable")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/backend-runtime-profiles/"), "/")
	if id == "" || len(id) > 64 {
		jsonError(w, http.StatusBadRequest, "invalid_request", "profile id is required")
		return
	}
	if r.Method != http.MethodDelete {
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "DELETE required")
		return
	}
	if err := s.settings.DeleteBackendRuntimeProfile(r.Context(), id); err != nil {
		if errors.Is(err, settings.ErrBackendRuntimeProfileNotFound) {
			jsonError(w, http.StatusNotFound, "not_found", "backend runtime profile not found")
			return
		}
		jsonError(w, http.StatusInternalServerError, "settings_write_failed", "failed to delete backend runtime profile")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) backendRuntimeProfileSnapshot(ctx context.Context, id, backend, modelID string) (map[string]interface{}, error) {
	if s.settings == nil {
		return nil, errors.New("settings storage is unavailable")
	}
	profile, err := s.settings.GetBackendRuntimeProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(strings.TrimSpace(profile.Backend)) != strings.ToLower(strings.TrimSpace(backend)) {
		return nil, errors.New("runtime profile backend does not match the benchmark backend")
	}
	if strings.TrimSpace(profile.ModelID) != strings.TrimSpace(modelID) {
		return nil, errors.New("runtime profile model_id does not match the benchmark model")
	}
	return map[string]interface{}{
		"profile_id": profile.ID,
		"profile_version": profile.Version,
		"source": profile.Source,
		"backend": profile.Backend,
		"model_id": profile.ModelID,
		"parameters": profile.Parameters,
		"updated_at": profile.UpdatedAt,
	}, nil
}
