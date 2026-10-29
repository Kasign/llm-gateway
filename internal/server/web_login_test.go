package server

import (
	"net/http"
	"testing"

	"github.com/whl/llm-gateway/internal/adminuser"
)

func TestValidateWebLoginPasswordFailsClosedWhenHashMissingOrInvalid(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{name: "缺少哈希", hash: ""},
		{name: "哈希格式无效", hash: "not-a-password-hash"},
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
