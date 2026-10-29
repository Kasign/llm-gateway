package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestRemoteProviderModelMappingUsesUpstreamModelID(t *testing.T) {
	s := &Server{
		cfg: &config.Config{},
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, DefaultModel: "public-chat", APIKeyHeader: "authorization", ModelMappings: map[string]string{"public-chat": "deepseek-chat"}},
		},
	}
	target, err := s.resolveProxyModelTargetForUpstream(context.Background(), settings.Upstream{Backend: "deepseek"})
	if err != nil { t.Fatal(err) }
	if target.ModelKey != "public-chat" || target.Hint != "public-chat" || target.UpstreamID != "deepseek-chat" {
		t.Fatalf("mapping target = %+v", target)
	}
}

func TestRemoteProviderModelMappingKeepsLegacyDefaultWhenAbsent(t *testing.T) {
	s := &Server{
		cfg: &config.Config{},
		webProviders: map[string]managedProvider{
			"legacy": {ID: "legacy", Enabled: true, DefaultModel: "legacy-model", APIKeyHeader: "authorization"},
		},
	}
	target, err := s.resolveProxyModelTargetForUpstream(context.Background(), settings.Upstream{Backend: "legacy"})
	if err != nil { t.Fatal(err) }
	if target.ModelKey != "legacy-model" || target.UpstreamID != "legacy-model" {
		t.Fatalf("legacy target = %+v", target)
	}
}

func TestRemoteProviderUsesRequestedPublicModelAlias(t *testing.T) {
	s := &Server{
		cfg: &config.Config{},
		upstream: settings.Upstream{Backend: "deepseek", ModelName: "public-default", BaseURL: "https://api.deepseek.com/v1"},
		webProviders: map[string]managedProvider{
			"deepseek": {
				ID: "deepseek", Enabled: true, DefaultModel: "public-default", APIKeyHeader: "authorization",
				BaseURL: "https://api.deepseek.com/v1",
				ModelMappings: map[string]string{
					"public-default": "deepseek-chat",
					"public-reasoner": "deepseek-reasoner",
				},
			},
		},
	}
	choice, err := s.resolveInference(context.Background(), core.State{Request: core.Request{ID: "mapped-request"}, ClientModel: "public-reasoner"})
	if err != nil { t.Fatal(err) }
	if choice.ModelKey != "public-reasoner" || choice.UpstreamID != "deepseek-reasoner" {
		t.Fatalf("requested model alias was not routed: %+v", choice)
	}
}
