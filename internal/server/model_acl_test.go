package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
)

func TestModelAllowedForKey(t *testing.T) {
	tests := []struct {
		name       string
		key        *auth.Key
		candidates []string
		want       bool
	}{
		{name: "nil key is unrestricted", key: nil, candidates: []string{"model-a"}, want: true},
		{name: "empty allow list is unrestricted", key: &auth.Key{}, candidates: []string{"model-a"}, want: true},
		{name: "model key match", key: &auth.Key{AllowedModels: []string{"model-a"}}, candidates: []string{"model-a", "upstream-a"}, want: true},
		{name: "upstream id match", key: &auth.Key{AllowedModels: []string{" upstream-a "}}, candidates: []string{"model-a", "upstream-a"}, want: true},
		{name: "different model is rejected", key: &auth.Key{AllowedModels: []string{"model-b"}}, candidates: []string{"model-a", "upstream-a"}, want: false},
		{name: "service ID match", key: &auth.Key{AllowedModels: []string{"deepseek"}}, candidates: []string{"deepseek", "deepseek-chat"}, want: true},
		{name: "different service ID is rejected", key: &auth.Key{AllowedModels: []string{"ollama"}}, candidates: []string{"deepseek", "deepseek-chat"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelAllowedForKey(tc.key, tc.candidates...); got != tc.want {
				t.Fatalf("modelAllowedForKey() = %v, want %v", got, tc.want)
			}
		})
	}
}


// 已选择的服务 ID 必须在推理 Guard 中生效，而不只是影响模型列表显示。
func TestAllowModelsGuardRestrictsProviderService(t *testing.T) {
	state := core.State{Model: core.ModelChoice{
		ProviderID: "deepseek", ModelKey: "deepseek-chat", UpstreamID: "deepseek-chat", Hint: "deepseek-chat",
	}}
	guard := (&Server{}).allowModelsGuard(&auth.Key{AllowedModels: []string{"deepseek"}})
	if err := guard(context.Background(), state); err != nil {
		t.Fatalf("被选中的服务应允许调用：%v", err)
	}
	guard = (&Server{}).allowModelsGuard(&auth.Key{AllowedModels: []string{"ollama"}})
	if err := guard(context.Background(), state); err == nil {
		t.Fatal("未选中的服务必须被拒绝")
	}
}
