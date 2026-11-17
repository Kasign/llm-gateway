package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/tools/coordinator"
	"github.com/whl/llm-gateway/internal/tools/modules/searxng"
	toolruntime "github.com/whl/llm-gateway/internal/tools/runtime"
)

func (s *Server) toolModelAllowed(st core.State) bool {
	if s == nil || s.cfg == nil || !s.cfg.Tools.Enabled {
		return false
	}
	providerID := strings.ToLower(strings.TrimSpace(st.Model.ProviderID))
	modelKey := strings.TrimSpace(st.Model.ModelKey)
	if providerID == "" || modelKey == "" {
		return false
	}
	for _, item := range s.cfg.Tools.AllowedModels {
		if strings.EqualFold(item.ProviderID, providerID) && item.ModelKey == modelKey {
			return true
		}
	}
	return false
}

func (s *Server) toolModelBudgetBlocked(providerID string) bool {
	if s == nil {
		return false
	}
	cfg, ok := s.remoteProviderConfig(providerID)
	return ok && cfg.TokenBudgetPerDay > 0
}

func (f inferenceForwarder) forwardWithTools(
	ctx context.Context,
	st core.State,
	p provider.Provider,
	req *http.Request,
	upPath string,
	rw http.ResponseWriter,
) (core.Result, error, bool) {
	s := f.s
	clientIntent := requestHasToolIntent(st.Body)
	chatPath := isChatCompletionsPath(st.Path)
	if clientIntent && (!chatPath || st.Stream) {
		return core.Result{}, &core.Error{Status: http.StatusBadRequest, Code: "tool_request_unsupported", Message: "tool calling is only supported for non-streaming Chat Completions"}, true
	}
	if !chatPath || st.Stream {
		return core.Result{}, nil, false
	}
	if s == nil || s.cfg == nil || !s.cfg.Tools.Enabled {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusForbidden, Code: "tool_calling_disabled", Message: "tool calling is not enabled"}, true
		}
		return core.Result{}, nil, false
	}
	modelAllowed := s.toolModelAllowed(st)
	if !modelAllowed {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusForbidden, Code: "tool_model_not_allowed", Message: "tool calling is not enabled for the active model"}, true
		}
		return core.Result{}, nil, false
	}
	if !clientIntent && !requestUsesSingleChoice(st.Body) {
		// 多候选请求不能进入工具循环；没有明确工具意图时保留原 Provider 行为。
		return core.Result{}, nil, false
	}
	if s.toolInitErr != nil || s.toolRegistry == nil || s.toolRuntime == nil {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_system_unavailable", Message: "tool system is unavailable"}, true
		}
		return core.Result{}, nil, false
	}
	if !p.Capabilities().ChatCompletions {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_provider_unsupported", Message: "active Provider does not support Chat Completions"}, true
		}
		return core.Result{}, nil, false
	}
	if s.toolModelBudgetBlocked(s.providerIDForState(st)) {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_token_budget_unsupported", Message: "tool calling is disabled for Providers with daily token budgets"}, true
		}
		return core.Result{}, nil, false
	}
	allowedNames := s.configuredToolNames(st)
	// 调用方 Key 的授权与全局/模型白名单取交集；缺少请求认证上下文时 fail closed。
	call := s.callGet(st.ID)
	if call == nil || call.key == nil || len(call.key.AllowedTools) == 0 {
		allowedNames = nil
	} else {
		granted := make(map[string]struct{}, len(call.key.AllowedTools))
		for _, name := range call.key.AllowedTools { granted[name] = struct{}{} }
		filtered := make([]string, 0, len(allowedNames))
		for _, name := range allowedNames { if _, ok := granted[name]; ok { filtered = append(filtered, name) } }
		allowedNames = filtered
	}
	if len(allowedNames) == 0 {
		if !clientIntent { return core.Result{}, nil, false }
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_registry_unavailable", Message: "no configured tools are available"}, true
		}
		return core.Result{}, nil, false
	}

	modelCaller := &bufferedModelCaller{
		requestID: st.ID,
		providerID: s.providerIDForState(st),
		provider: p,
		request: req,
		path: upPath,
		modelID: st.Model.UpstreamID,
		onDispatch: func() {
			s.markProviderTokenBudgetDispatched(st.ID)
		},
	}
	toolCoordinator, err := coordinator.New(s.toolRegistry, s.toolRuntime, modelCaller)
	if err != nil {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_coordinator_unavailable", Message: "tool coordinator is unavailable"}, true
		}
		return core.Result{}, nil, false
	}
	toolCtx := withRequestToolAllowlist(ctx, allowedNames)
	result, runErr := toolCoordinator.Run(toolCtx, st.Body, allowedNames)
	s.storeCoordinatorUsage(st.ID, result)

	response := result.Response
	if response.StatusCode == 0 || len(response.Body) == 0 {
		if runErr != nil {
			return core.Result{}, mapToolCoordinatorError(runErr), true
		}
		return core.Result{}, &core.Error{Status: http.StatusBadGateway, Code: "tool_response_missing", Message: "tool coordinator returned no final model response"}, true
	}
	coreResult := core.Result{
		Status: response.StatusCode,
		UpstreamStatus: response.UpstreamStatus,
		ResponseBytes: int64(len(response.Body)),
		Body: append([]byte(nil), response.Body...),
	}
	if coreResult.UpstreamStatus == 0 {
		coreResult.UpstreamStatus = coreResult.Status
	}
	if writeErr := writeBufferedModelResponse(rw, response); writeErr != nil {
		s.failRun(st.ID, "工具模式最终响应写回失败")
		return coreResult, writeErr, true
	}
	return coreResult, mapToolCoordinatorError(runErr), true
}

func writeBufferedModelResponse(w http.ResponseWriter, response coordinator.ModelResponse) error {
	if w == nil {
		return errors.New("client response writer is nil")
	}
	if response.StatusCode < 100 || response.StatusCode > 599 {
		return errors.New("buffered Provider returned an invalid HTTP status")
	}
	if state, ok := w.(interface{ HeaderWritten() bool }); ok && state.HeaderWritten() {
		return errors.New("client response was already committed before final model response")
	}
	header := w.Header()
	for key, values := range response.Header {
		canonical := http.CanonicalHeaderKey(key)
		switch strings.ToLower(canonical) {
		case "connection", "proxy-connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "content-length":
			continue
		}
		header[canonical] = append([]string(nil), values...)
	}
	for _, connectionValue := range response.Header.Values("Connection") {
		for _, name := range strings.Split(connectionValue, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	header.Del("Content-Length")
	w.WriteHeader(response.StatusCode)
	n, err := w.Write(response.Body)
	if err == nil && n != len(response.Body) {
		err = errors.New("client response write was incomplete")
	}
	return err
}

func mapToolCoordinatorError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, toolruntime.ErrExecutionTimeout) || errors.Is(err, searxng.ErrUpstreamTimeout) {
		return &core.Error{Status: http.StatusGatewayTimeout, Code: "tool_execution_timeout", Message: "tool execution timed out"}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if mapped := provider.ToCoreError(err); mapped != nil {
		return mapped
	}
	switch {
	case errors.Is(err, toolruntime.ErrUnauthorized):
		return &core.Error{Status: http.StatusForbidden, Code: "tool_not_allowed", Message: "tool execution is not allowed"}
	case errors.Is(err, toolruntime.ErrAuthorizationUnavailable):
		return &core.Error{Status: http.StatusServiceUnavailable, Code: "tool_authorization_unavailable", Message: "tool authorization is unavailable"}
	case errors.Is(err, toolruntime.ErrConcurrencyLimit):
		return &core.Error{Status: http.StatusTooManyRequests, Code: "tool_execution_busy", Message: "tool execution capacity is exhausted"}
	case errors.Is(err, coordinator.ErrUnsupportedRequest), errors.Is(err, coordinator.ErrInvalidRequest):
		return &core.Error{Status: http.StatusBadRequest, Code: "tool_request_unsupported", Message: "tool request is not supported"}
	case errors.Is(err, coordinator.ErrNoAllowedTools):
		return &core.Error{Status: http.StatusForbidden, Code: "tool_not_allowed", Message: "no tools are authorized for this request"}
	default:
		return &core.Error{Status: http.StatusBadGateway, Code: "tool_call_failed", Message: "tool request failed"}
	}
}
