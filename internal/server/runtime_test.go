package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestLocalRuntimeBackendRejectsRemoteProvider(t *testing.T) {
	for _, id := range []string{"deepseek", "openai"} {
		if got, err := localRuntimeBackend(id); err == nil || got != "" {
			t.Errorf("localRuntimeBackend(%q) = %q, %v; want rejection", id, got, err)
		}
	}
	for _, id := range []string{"llamacpp", "ollama"} {
		if got, err := localRuntimeBackend(id); err != nil || got != id {
			t.Errorf("localRuntimeBackend(%q) = %q, %v; want local backend", id, got, err)
		}
	}
}

func TestBuildAdminRuntimeOverviewRemoteDoesNotExposeLocalModelState(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
		},
		upstream: settings.Upstream{
		Backend: "deepseek", BaseURL: "https://api.deepseek.com/v1", ModelName: "deepseek-chat",
	}}
	got, err := s.buildAdminRuntimeOverview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["remote_provider"] != true || got["local_model_management"] != false {
		t.Fatalf("remote runtime capabilities = %#v", got)
	}
	if got["backend"] != "deepseek" || got["active_model"] != "deepseek-chat" {
		t.Fatalf("remote runtime identity = %#v", got)
	}
	if _, ok := got["loaded"].([]interface{}); !ok {
		t.Fatalf("remote runtime should not expose stale local loaded model records: %#v", got)
	}
}


// 管理 Runtime API 必须使用 Web Session；部署 Admin API Key 不能读取运行时状态。
func TestRuntimeAPIRejectsAdminAPIKey(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.AdminAPIKey = "test-admin-api-key"
	s := &Server{cfg: cfg}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/runtime", nil)
	req.Header.Set("X-Admin-Key", "test-admin-api-key")
	rec := httptest.NewRecorder()
	s.runtimeAPI(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("runtimeAPI with Admin API Key returned %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}


// server_metrics 只属于本地 llama.cpp；切换到其它后端时必须清除旧缓存字段。
func TestAttachRuntimeServerMetricsOnlyForLlamacpp(t *testing.T) {
	metrics := map[string]interface{}{"status": "complete"}
	for _, backend := range []string{"llamacpp", "ollama", "deepseek", "openai", "unknown"} {
		t.Run(backend, func(t *testing.T) {
			snapshot := map[string]interface{}{"backend": backend, "server_metrics": "stale"}
			attachRuntimeServerMetrics(snapshot, backend, metrics)
			got, exists := snapshot["server_metrics"]
			if strings.EqualFold(backend, "llamacpp") {
				gotMetrics, ok := got.(map[string]interface{})
				if !exists || !ok || gotMetrics["status"] != "complete" {
					t.Fatalf("llamacpp metrics = %#v, exists=%v; want supplied metrics", got, exists)
				}
			} else if exists {
				t.Fatalf("%s runtime leaked local server_metrics: %#v", backend, got)
			}
		})
	}
}

func TestAttachRuntimeServerMetricsHandlesNilSnapshot(t *testing.T) {
	attachRuntimeServerMetrics(nil, "llamacpp", map[string]interface{}{"status": "complete"})
}
