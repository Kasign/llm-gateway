package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/ratelimit"
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
	s := &Server{
		cfg:      cfg,
		audit:    writer,
		webLogin: webLoginSnapshot{Username: webAdminIdentity, Revision: 1},
	}

	unauthorized := httptest.NewRecorder()
	s.auditMetricsAPI(unauthorized, httptest.NewRequest(http.MethodGet, "/api/admin/audit/metrics", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorized.Code)
	}

	// 管理员 API Key 只能管理服务密钥；其他管理端点必须使用网页登录会话。
	adminKeyRequest := httptest.NewRequest(http.MethodGet, "/api/admin/audit/metrics", nil)
	adminKeyRequest.Header.Set("X-Admin-Key", "test-admin-key")
	adminKeyRecorder := httptest.NewRecorder()
	s.auditMetricsAPI(adminKeyRecorder, adminKeyRequest)
	if adminKeyRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("admin API Key status = %d, want 401 (key is scoped to /api/admin/keys)", adminKeyRecorder.Code)
	}

	// Web Session 存在 Redis 中；没有测试 Redis 时仍执行前面的鉴权边界断言。
	addr := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDR"))
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Web Session success assertions")
	}
	limiter := ratelimit.New(addr, "", 0)
	t.Cleanup(func() { _ = limiter.Close() })
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer pingCancel()
	if err := limiter.Ping(pingCtx); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}
	s.limiter = limiter
	token, err := adminuser.NewSessionToken()
	if err != nil {
		t.Fatalf("create session token: %v", err)
	}
	if err := limiter.SetSession(context.Background(), token, webAdminIdentity, 1, time.Minute); err != nil {
		t.Fatalf("save test session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
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
	post.Header.Set("Authorization", "Bearer "+token)
	methodRecorder := httptest.NewRecorder()
	s.auditMetricsAPI(methodRecorder, post)
	if methodRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", methodRecorder.Code)
	}
	if methodRecorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", methodRecorder.Header().Get("Allow"))
	}
}
