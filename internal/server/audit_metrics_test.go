package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/config"
)

func TestAuditMetricsAPIRequiresAdminAndReturnsReadOnlySnapshot(t *testing.T) {
	writer := audit.NewWriter(nil, 8)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = writer.Shutdown(ctx)
	})

	cfg := &config.Config{}
	cfg.Auth.AdminAPIKey = "test-admin-key"
	s := &Server{cfg: cfg, audit: writer}

	unauthorized := httptest.NewRecorder()
	s.auditMetricsAPI(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/audit/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit/metrics", nil)
	req.Header.Set("X-Admin-Key", "test-admin-key")
	recorder := httptest.NewRecorder()
	s.auditMetricsAPI(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if strings.Contains(recorder.Body.String(), "test-admin-key") {
		t.Fatal("response must not contain the admin key")
	}

	var body struct {
		Data struct {
			QueueDepth    int     `json:"queue_depth"`
			QueueCapacity int     `json:"queue_capacity"`
			DropRate      float64 `json:"drop_rate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.QueueCapacity != 8 || body.Data.QueueDepth != 0 || body.Data.DropRate != 0 {
		t.Fatalf("metrics data = %+v, want empty queue of capacity 8", body.Data)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/admin/audit/metrics", nil)
	post.Header.Set("X-Admin-Key", "test-admin-key")
	methodRecorder := httptest.NewRecorder()
	s.auditMetricsAPI(methodRecorder, post)
	if methodRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", methodRecorder.Code)
	}
	if methodRecorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", methodRecorder.Header().Get("Allow"))
	}
}
