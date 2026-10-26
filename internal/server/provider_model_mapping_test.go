package server

import (
	"context"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestRemoteProviderModelMappingUsesUpstreamModelID(t *testing.T) {
	s := &Server{cfg:&config.Config{}}
	s.cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{ID:"deepseek",Enabled:true,DefaultModel:"public-chat",ModelMappings:map[string]string{"public-chat":"deepseek-chat"}}}
	target, err := s.resolveProxyModelTargetForUpstream(context.Background(), settings.Upstream{Backend:"deepseek"})
	if err != nil { t.Fatal(err) }
	if target.ModelKey!="public-chat" || target.Hint!="public-chat" || target.UpstreamID!="deepseek-chat" { t.Fatalf("mapping target = %+v",target) }
}

func TestRemoteProviderModelMappingKeepsLegacyDefaultWhenAbsent(t *testing.T) {
	s := &Server{cfg:&config.Config{}}
	s.cfg.Providers.OpenAICompatible=[]config.OpenAICompatibleProviderConfig{{ID:"legacy",Enabled:true,DefaultModel:"legacy-model"}}
	target,err:=s.resolveProxyModelTargetForUpstream(context.Background(),settings.Upstream{Backend:"legacy"})
	if err!=nil {t.Fatal(err)}
	if target.ModelKey!="legacy-model"||target.UpstreamID!="legacy-model" {t.Fatalf("legacy target = %+v",target)}
}
