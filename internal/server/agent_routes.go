package server

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
)

type agentRouteContextKey struct{}

// agentRoute 保存经过配置校验的路由元数据。外部请求不能通过 Header/body 覆盖 Provider。
type agentRoute struct {
	ID              string
	PathPrefix      string
	ProviderID      string
	DefaultModel    string
	OriginalPath    string
	OriginalRawPath string
}

func agentRouteFromContext(ctx context.Context) (agentRoute, bool) {
	if ctx == nil {
		return agentRoute{}, false
	}
	route, ok := ctx.Value(agentRouteContextKey{}).(agentRoute)
	return route, ok
}

// agentRouteHandler 将 /{prefix}/v1/* 归一成标准 /v1/*，通过私有 Context 固定上游。
func (s *Server) agentRouteHandler(cfg config.AgentRouteConfig, next http.Handler) http.Handler {
	stripped := http.StripPrefix(cfg.PathPrefix, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := agentRoute{
			ID: cfg.ID, PathPrefix: cfg.PathPrefix, ProviderID: cfg.ProviderID, DefaultModel: cfg.DefaultModel,
			OriginalPath: r.URL.Path, OriginalRawPath: r.URL.RawPath,
		}
		ctx := context.WithValue(r.Context(), agentRouteContextKey{}, route)
		stripped.ServeHTTP(w, r.WithContext(ctx))
	})
}

// agentRequestPublicURL 在审计 URL 中恢复 Agent 前缀；普通 /v1 请求沿用原有 URL 与 query 脱敏。
func agentRequestPublicURL(r *http.Request, trusted []netip.Prefix) string {
	route, routed := agentRouteFromContext(r.Context())
	if !routed || route.OriginalPath == "" || r.URL == nil {
		return requestPublicURL(r, trusted)
	}
	copyRequest := r.Clone(r.Context())
	copiedURL := *r.URL
	copiedURL.Path = route.OriginalPath
	copiedURL.RawPath = route.OriginalRawPath
	copyRequest.URL = &copiedURL
	return requestPublicURL(copyRequest, trusted)
}

// resolveAgentRouteTarget 按 Agent 固定 Provider/默认模型构建目标，不更新全局活动后端。
func (s *Server) resolveAgentRouteTarget(ctx context.Context, route agentRoute) (proxyModelTarget, error) {
	var zero proxyModelTarget
	providerID := strings.ToLower(strings.TrimSpace(route.ProviderID))
	if providerID == "" || !s.isConfiguredProvider(providerID) {
		return zero, &core.Error{Status: http.StatusServiceUnavailable, Code: "agent_provider_unavailable", Message: "Agent Provider is not configured"}
	}
	modelID := strings.TrimSpace(route.DefaultModel)
	if modelID == "" {
		return zero, &core.Error{Status: http.StatusServiceUnavailable, Code: "agent_model_unconfigured", Message: "Agent default model is not configured"}
	}
	active := s.resolveUpstream(settings.Upstream{Backend: providerID, ModelName: modelID})
	if strings.TrimSpace(active.BaseURL) == "" {
		return zero, &core.Error{Status: http.StatusServiceUnavailable, Code: "agent_provider_unavailable", Message: "Agent Provider URL is not configured"}
	}
	return s.resolveProxyModelTargetForUpstream(ctx, active)
}
