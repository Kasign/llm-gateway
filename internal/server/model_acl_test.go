package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/auth"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelAllowedForKey(tc.key, tc.candidates...); got != tc.want {
				t.Fatalf("modelAllowedForKey() = %v, want %v", got, tc.want)
			}
		})
	}
}
