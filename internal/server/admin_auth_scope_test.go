package server

import "testing"

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
