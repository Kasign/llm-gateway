package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
)

func TestAdminAPIKeyAllowedForPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "列出和创建服务密钥", path: "/api/admin/keys", want: true},
		{name: "轮换单个服务密钥", path: "/api/admin/keys/123/rotate", want: true},
		{name: "更新单个服务密钥", path: "/api/admin/keys/123", want: true},
		{name: "不得授权运行时管理", path: "/api/admin/runtime", want: false},
		{name: "不得授权 Provider 管理", path: "/api/admin/providers", want: false},
		{name: "不得授权请求日志", path: "/api/requests", want: false},
		{name: "相似路径不得绕过限制", path: "/api/admin/keys-extra", want: false},
		{name: "客户端服务 API 不属于管理密钥范围", path: "/v1/models", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := adminAPIKeyAllowedForPath(tt.path); got != tt.want {
				t.Fatalf("adminAPIKeyAllowedForPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}


// 管理员 API Key 只能授权服务密钥管理端点；其它管理 API 必须依赖有效 Web Session。
func TestAdminAuthorizedRestrictsAdminAPIKeyToServiceKeys(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.AdminAPIKey = "test-admin-api-key"
	s := &Server{cfg: cfg}

	tests := []struct {
		name string
		path string
		key  string
		want bool
	}{
		{name: "服务密钥列表允许正确管理员 Key", path: "/api/admin/keys", key: "test-admin-api-key", want: true},
		{name: "单个服务密钥操作允许正确管理员 Key", path: "/api/admin/keys/123/rotate", key: "test-admin-api-key", want: true},
		{name: "错误管理员 Key 被拒绝", path: "/api/admin/keys", key: "wrong-admin-api-key", want: false},
		{name: "管理员 Key 不能授权运行时管理", path: "/api/admin/runtime", key: "test-admin-api-key", want: false},
		{name: "管理员 Key 不能授权 Provider 管理", path: "/api/admin/providers", key: "test-admin-api-key", want: false},
		{name: "管理员 Key 不能授权密码设置", path: "/api/auth/credentials", key: "test-admin-api-key", want: false},
		{name: "管理员 Key 不能授权请求记录", path: "/api/requests", key: "test-admin-api-key", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("X-Admin-Key", tt.key)
			if got := s.adminAuthorized(context.Background(), req); got != tt.want {
				t.Fatalf("adminAuthorized(%q, X-Admin-Key) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
