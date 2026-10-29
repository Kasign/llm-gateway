package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/config"
)

func TestValidateWebLoginPasswordFailsClosedWhenHashMissingOrInvalid(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{name: "缺少哈希", hash: ""},
		{name: "哈希格式无效", hash: "not-a-password-hash"},
		{name: "仅有哈希前缀但结构损坏", hash: "pbkdf2:broken"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := webLoginSnapshot{Username: webAdminIdentity, PasswordEnabled: true, PasswordHash: tt.hash}
			status, code, _ := validateWebLoginPassword(snap, "any-password")
			if status != http.StatusServiceUnavailable || code != "login_not_configured" {
				t.Fatalf("got status=%d code=%q, want 503 login_not_configured", status, code)
			}
		})
	}
}

func TestValidateWebLoginPasswordEnabledAndDisabledModes(t *testing.T) {
	hash, err := adminuser.HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		snap    webLoginSnapshot
		password string
		wantStatus int
		wantCode string
	}{
		{name: "正确密码", snap: webLoginSnapshot{Username: webAdminIdentity, PasswordEnabled: true, PasswordHash: hash}, password: "correct-password"},
		{name: "错误密码", snap: webLoginSnapshot{Username: webAdminIdentity, PasswordEnabled: true, PasswordHash: hash}, password: "wrong-password", wantStatus: http.StatusUnauthorized, wantCode: "invalid_credentials"},
		{name: "显式关闭密码保护", snap: webLoginSnapshot{Username: webAdminIdentity, PasswordEnabled: false}, password: "", wantStatus: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, _ := validateWebLoginPassword(tt.snap, tt.password)
			if status != tt.wantStatus || code != tt.wantCode {
				t.Fatalf("got status=%d code=%q, want status=%d code=%q", status, code, tt.wantStatus, tt.wantCode)
			}
		})
	}
}


func TestLoadWebLoginStateResetFromEnvBypassesDatabase(t *testing.T) {
	hash, err := adminuser.HashPassword("deployment-password")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.Auth.AdminUsername = "admin"
	cfg.Auth.AdminPasswordHash = hash
	cfg.Auth.WebPasswordEnabled = true
	cfg.Auth.WebLoginResetFromEnv = true

	// reset-from-env 应跳过数据库读取；settings 故意保持 nil，确保测试能捕获误访问。
	s := &Server{cfg: cfg}
	s.loadWebLoginState(context.Background())

	snap := s.currentWebLoginSnapshot()
	if snap.PasswordSource != "deployment" {
		t.Fatalf("password source = %q, want deployment", snap.PasswordSource)
	}
	if !snap.PasswordEnabled || snap.PasswordHash != hash {
		t.Fatalf("deployment login settings were not restored: enabled=%v hash_matches=%v", snap.PasswordEnabled, snap.PasswordHash == hash)
	}
	if snap.Revision != 0 {
		t.Fatalf("deployment revision = %d, want 0", snap.Revision)
	}
}

func TestSessionMatchesWebLoginRejectsStaleRevision(t *testing.T) {
	s := &Server{
		webLogin: webLoginSnapshot{
			Username:        webAdminIdentity,
			PasswordHash:    "must-not-be-exposed",
			PasswordEnabled: true,
			PasswordSource:  "database",
			Revision:        12,
		},
	}

	tests := []struct {
		name     string
		username string
		revision uint64
		want     bool
	}{
		{name: "当前会话版本", username: webAdminIdentity, revision: 12, want: true},
		{name: "旧密码会话版本", username: webAdminIdentity, revision: 11, want: false},
		{name: "未来会话版本", username: webAdminIdentity, revision: 13, want: false},
		{name: "用户名不匹配", username: "other", revision: 12, want: false},
		{name: "用户名未初始化", username: "", revision: 12, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.sessionMatchesWebLogin(tt.username, tt.revision); got != tt.want {
				t.Fatalf("sessionMatchesWebLogin(%q, %d) = %v, want %v", tt.username, tt.revision, got, tt.want)
			}
		})
	}
}

func TestAuthStatusAPIExposesOnlyNonSecretLoginState(t *testing.T) {
	s := &Server{
		webLogin: webLoginSnapshot{
			Username:        webAdminIdentity,
			PasswordHash:    "must-not-be-exposed",
			PasswordEnabled: true,
			PasswordSource:  "database",
			Revision:        4,
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rec := httptest.NewRecorder()

	s.authStatusAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	body := rec.Body.String()
	for _, secret := range []string{"must-not-be-exposed", "password_hash", "username", "revision"} {
		if strings.Contains(body, secret) {
			t.Fatalf("auth status leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, ""password_enabled":true") || !strings.Contains(body, ""password_source":"database"") {
		t.Fatalf("auth status is missing public login state: %s", body)
	}
}
