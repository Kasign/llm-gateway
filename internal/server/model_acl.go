package server

import (
	"strings"

	"github.com/whl/llm-gateway/internal/auth"
)

// modelAllowedForKey 集中定义调用方的模型 ACL 语义，供推理 Guard 和 /v1/models 共用。
// 空 allowed_models 表示不限制；非空时 model_key、上游模型 ID 或路由 hint 任一匹配即可。
func modelAllowedForKey(key *auth.Key, candidates ...string) bool {
	if key == nil || len(key.AllowedModels) == 0 {
		return true
	}
	for _, allowed := range key.AllowedModels {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate = strings.TrimSpace(candidate); candidate != "" && candidate == allowed {
				return true
			}
		}
	}
	return false
}
