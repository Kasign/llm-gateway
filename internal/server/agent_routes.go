package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/auth"
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


// agentModelListBody 生成 Agent 的 /v1/models 响应，只公开该 Agent 固定的目标模型，并执行调用方 Key 的模型 ACL。
func agentModelListBody(route agentRoute, target proxyModelTarget, key *auth.Key) []byte {
	modelID := strings.TrimSpace(target.ModelKey)
	if target.Model != nil {
		modelID = strings.TrimSpace(target.Model.ModelKey)
	}
	data := make([]map[string]interface{}, 0, 1)
	if modelID != "" && modelAllowedForKey(key, modelID, target.UpstreamID, target.Hint) {
		data = append(data, map[string]interface{}{
			"id": modelID, "object": "model", "created": time.Now().Unix(),
			"owned_by": route.ProviderID, "display_name": target.UpstreamID,
		})
	}
	body, _ := json.Marshal(map[string]interface{}{"object": "list", "data": data})
	return body
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
