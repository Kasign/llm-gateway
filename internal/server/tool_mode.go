package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/store"
	"github.com/whl/llm-gateway/internal/tools/coordinator"
	"github.com/whl/llm-gateway/internal/tools/modules/searxng"
	toolruntime "github.com/whl/llm-gateway/internal/tools/runtime"
)

func (s *Server) toolModelAllowed(st core.State, key *auth.Key) bool {
	if s == nil || s.cfg == nil || !s.cfg.Tools.Enabled || key == nil {
		return false
	}
	providerID := strings.ToLower(strings.TrimSpace(st.Model.ProviderID))
	modelKey := strings.TrimSpace(st.Model.ModelKey)
	if providerID == "" || modelKey == "" {
		return false
	}
	// 模型范围复用 Web 服务密钥的 allowed_models：空列表不限制，非空列表按现有模型 ACL 匹配。
	return modelAllowedForKey(key, providerID, modelKey, st.Model.UpstreamID)
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

	// 工具能力只接管明确支持的 Chat Completions 请求；其他协议必须保持原 Provider 透传语义。
	if !chatPath {
		return core.Result{}, nil, false
	}
	// 工具功能关闭时完全旁路，不改变上游原本对 tools / functions 字段的处理行为。
	if s == nil || s.cfg == nil || !s.cfg.Tools.Enabled {
		return core.Result{}, nil, false
	}
	if st.Stream {
		if clientIntent {
			return core.Result{}, &core.Error{Status: http.StatusBadRequest, Code: "tool_request_unsupported", Message: "tool calling is only supported for non-streaming Chat Completions"}, true
		}
		return core.Result{}, nil, false
	}
	call := s.callGet(st.ID)
	var requestKey *auth.Key
	if call != nil {
		requestKey = call.key
	}
	modelAllowed := s.toolModelAllowed(st, requestKey)
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
	// 当前 Key 的模型范围和工具授权共同生效；缺少请求认证上下文时 fail closed。
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
		if call == nil || call.key == nil || len(call.key.AllowedTools) == 0 {
			return core.Result{}, &core.Error{Status: http.StatusForbidden, Code: "tool_not_allowed", Message: "this API key is not authorized to use tools"}, true
		}
		return core.Result{}, &core.Error{Status: http.StatusForbidden, Code: "tool_not_allowed", Message: "no configured tools are authorized for this API key"}, true
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
	var auditEvents []store.ToolCallEvent
	result, runErr := toolCoordinator.RunWithObserver(toolCtx, st.Body, allowedNames, func(event coordinator.ToolCallEvent) {
		auditEvents = append(auditEvents, mapToolCallAuditEvent(event))
	})
	if len(auditEvents) > 0 && s.audit != nil {
		s.audit.InsertToolCallEvents(st.ID, auditEvents)
	}
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

// mapToolCallAuditEvent 将 Coordinator/Runtime 错误映射为稳定审计分类，不保存原始错误文本。
func mapToolCallAuditEvent(event coordinator.ToolCallEvent) store.ToolCallEvent {
	status, code := event.Status, event.ErrorCode
	err := event.Cause
	switch {
	case errors.Is(err, toolruntime.ErrUnauthorized):
		status, code = "rejected", "tool_unauthorized"
	case errors.Is(err, toolruntime.ErrAuthorizationUnavailable):
		status, code = "rejected", "tool_authorization_unavailable"
	case errors.Is(err, toolruntime.ErrConcurrencyLimit):
		status, code = "rejected", "tool_concurrency_limit"
	case errors.Is(err, toolruntime.ErrExecutionTimeout), errors.Is(err, searxng.ErrUpstreamTimeout):
		status, code = "timed_out", "tool_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		status, code = "timed_out", "tool_timeout"
	case errors.Is(err, context.Canceled):
		status, code = "cancelled", "tool_cancelled"
	case errors.Is(err, toolruntime.ErrResultTooLarge):
		status, code = "failed", "tool_result_too_large"
	case errors.Is(err, toolruntime.ErrInvalidResult):
		status, code = "failed", "tool_result_invalid"
	case errors.Is(err, toolruntime.ErrExecutorPanicked):
		status, code = "failed", "tool_executor_panicked"
	case errors.Is(err, toolruntime.ErrExecutionFailed):
		status, code = "failed", "tool_execution_failed"
	}
	return store.ToolCallEvent{
		Seq: event.Seq, Round: event.Round, ToolName: event.ToolName,
		Status: status, DurationMS: event.DurationMS, ResultBytes: event.ResultBytes,
		ErrorCode: code,
	}
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
