package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/tools"
	"github.com/whl/llm-gateway/internal/tools/coordinator"
	toolruntime "github.com/whl/llm-gateway/internal/tools/runtime"
)

func TestWriteBufferedModelResponseCopiesSafeHeadersAndBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	response := coordinator.ModelResponse{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Connection": []string{"keep-alive", "X-Hop"},
			"X-Hop": []string{"must-not-pass"},
			"Content-Length": []string{"999"},
		},
		Body: []byte(`{"ok":true}`),
	}
	if err := writeBufferedModelResponse(recorder, response); err != nil {
		t.Fatalf("writeBufferedModelResponse() error = %v", err)
	}
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" ||
		recorder.Header().Get("X-Hop") != "" || recorder.Header().Get("Content-Length") != "" ||
		recorder.Body.String() != string(response.Body) {
		t.Fatalf("unexpected client response: code=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestToolModelAllowedUsesProviderAndStableModelKey(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	s.cfg.Tools.Enabled = true
	s.cfg.Tools.AllowedModels = []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}}
	if !s.toolModelAllowed(core.State{Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "changed-upstream-id"}}) {
		t.Fatal("stable provider/model key allowlist was not matched")
	}
	if s.toolModelAllowed(core.State{Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "other-key", UpstreamID: "changed-upstream-id"}}) {
		t.Fatal("unlisted model key was allowed")
	}
}

func TestToolModelBudgetBlockedForRemoteDailyBudget(t *testing.T) {
	s := &Server{webProviders: map[string]managedProvider{
		"remote": {ID: "remote", Enabled: true, TokenBudgetPerDay: 1000},
	}}
	if !s.toolModelBudgetBlocked("remote") {
		t.Fatal("remote Provider with daily Token Budget must remain disabled for tool calls")
	}
}

func TestMapToolCoordinatorErrorDoesNotExposeExecutorText(t *testing.T) {
	err := &coordinator.ToolExecutionError{ToolName: "web.search", Cause: errors.New("secret upstream body")}
	mapped := mapToolCoordinatorError(err)
	coreErr, ok := mapped.(*core.Error)
	if !ok || coreErr.Message != "tool request failed" || strings.Contains(coreErr.Message, "secret") {
		t.Fatalf("unexpected public error: %#v", mapped)
	}
}

type alreadyCommittedWriter struct {
	header http.Header
}

func (w *alreadyCommittedWriter) Header() http.Header { return w.header }
func (*alreadyCommittedWriter) Write([]byte) (int, error) { return 0, nil }
func (*alreadyCommittedWriter) WriteHeader(int) {}
func (*alreadyCommittedWriter) HeaderWritten() bool { return true }

func TestWriteBufferedModelResponseRejectsAlreadyCommittedWriter(t *testing.T) {
	writer := &alreadyCommittedWriter{header: make(http.Header)}
	err := writeBufferedModelResponse(writer, coordinator.ModelResponse{StatusCode: http.StatusOK, Body: []byte("{}")})
	if err == nil {
		t.Fatal("already committed response writer was accepted")
	}
}

type toolLoopTestExecutor struct{}

func (toolLoopTestExecutor) Execute(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"echo":"ok"}`), nil
}

type toolLoopTestProvider struct {
	calls int
	modelIDs []string
	firstTools []byte
}

func (*toolLoopTestProvider) ID() string { return "llamacpp" }
func (*toolLoopTestProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Protocol: "openai-compatible", ChatCompletions: true}
}
func (p *toolLoopTestProvider) Forward(_ context.Context, call provider.InferenceCall) (core.Result, error) {
	p.calls++
	p.modelIDs = append(p.modelIDs, call.ModelID)
	if call.Stream {
		return core.Result{}, errors.New("unexpected stream")
	}
	var body []byte
	if p.calls == 1 {
		p.firstTools = append([]byte(nil), call.Body...)
		body = []byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"test.echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	} else {
		body = []byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}
	call.Writer.Header().Set("Content-Type", "application/json")
	call.Writer.WriteHeader(http.StatusOK)
	if _, err := call.Writer.Write(body); err != nil {
		return core.Result{}, err
	}
	return core.Result{Status: http.StatusOK, UpstreamStatus: http.StatusOK, Body: body}, nil
}

func TestForwardWithToolsUsesOneProviderForAllRoundsAndWritesOnlyFinalResponse(t *testing.T) {
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name: "test.echo",
		Description: "Echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	if err := registry.Register(definition, toolLoopTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	providerInstance := &toolLoopTestProvider{}
	auditWriter := audit.NewWriter(nil, 8)
	s := &Server{
		audit: auditWriter,
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}},
			AllowedTools: []string{"test.echo"},
		}},
		toolRegistry: registry,
		toolRuntime: runtime,
	}
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	sink := &responseSink{w: recorder}
	state := core.State{
		Request: core.Request{
			ID: "request-1",
			Path: "/v1/chat/completions",
			Method: http.MethodPost,
			Body: []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"client.injected","parameters":{"type":"object"}}}]}`),
			Sink: sink,
		},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "active-model"},
	}
	s.callBegin("request-1", &auth.Key{AllowedTools: []string{"test.echo"}})
	forwarder := inferenceForwarder{s: s}
	result, runErr, handled := forwarder.forwardWithTools(context.Background(), state, providerInstance, req, "/v1/chat/completions", recorder)
	if !handled || runErr != nil {
		t.Fatalf("forwardWithTools() handled=%v error=%v", handled, runErr)
	}
	if providerInstance.calls != 2 || len(providerInstance.modelIDs) != 2 ||
		providerInstance.modelIDs[0] != "active-model" || providerInstance.modelIDs[1] != "active-model" {
		t.Fatalf("Provider/model was not reused: calls=%d modelIDs=%v", providerInstance.calls, providerInstance.modelIDs)
	}
	if result.Status != http.StatusOK || !strings.Contains(recorder.Body.String(), "done") ||
		strings.Contains(recorder.Body.String(), "tool_calls") || strings.Contains(recorder.Body.String(), "echo") {
		t.Fatalf("client did not receive only final response: status=%d body=%q", result.Status, recorder.Body.String())
	}
	if !strings.Contains(string(providerInstance.firstTools), "test.echo") ||
		strings.Contains(string(providerInstance.firstTools), "client.injected") {
		t.Fatalf("client tool definition was not replaced: %s", providerInstance.firstTools)
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := auditWriter.Flush(flushCtx); err != nil {
		t.Fatalf("flush tool audit event: %v", err)
	}
	if err := auditWriter.Shutdown(flushCtx); err != nil {
		t.Fatalf("shutdown audit writer: %v", err)
	}
	stats := auditWriter.Stats()
	if stats.ToolEventEnqueuedTotal != 1 || stats.ToolEventWriteFailedTotal != 1 {
		t.Fatalf("tool audit pipeline metrics enqueued/write_failed = %d/%d, want 1/1",
			stats.ToolEventEnqueuedTotal, stats.ToolEventWriteFailedTotal)
	}
}

func TestForwardWithToolsRejectsClientToolsWhenModelNotAllowlisted(t *testing.T) {
	s := &Server{cfg: &config.Config{Tools: config.ToolsConfig{Enabled: true}}}
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	state := core.State{
		Request: core.Request{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"x"}],"tools":[]}`),
		},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "not-allowed"},
	}
	_, err, handled := (inferenceForwarder{s: s}).forwardWithTools(context.Background(), state, &toolLoopTestProvider{}, req, "/v1/chat/completions", recorder)
	if !handled || err == nil {
		t.Fatalf("request was not rejected: handled=%v err=%v", handled, err)
	}
	if coreErr, ok := err.(*core.Error); !ok || coreErr.Status != http.StatusForbidden {
		t.Fatalf("unexpected rejection: %#v", err)
	}
}

func TestForwardWithToolsFallsBackForRemoteDailyBudgetWithoutClientToolIntent(t *testing.T) {
	s := &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "remote", ModelKey: "stable-alias"}},
			AllowedTools: []string{"web.search"},
		}},
		webProviders: map[string]managedProvider{
			"remote": {ID: "remote", Enabled: true, TokenBudgetPerDay: 1000},
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	state := core.State{
		Request: core.Request{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"ordinary request"}]}`),
		},
		Model: core.ModelChoice{ProviderID: "remote", ModelKey: "stable-alias", UpstreamID: "upstream-model"},
	}
	_, err, handled := (inferenceForwarder{s: s}).forwardWithTools(context.Background(), state, &toolLoopTestProvider{}, req, "/v1/chat/completions", httptest.NewRecorder())
	if handled || err != nil {
		t.Fatalf("ordinary request did not fall back: handled=%v err=%v", handled, err)
	}
}

func TestForwardWithToolsRejectsExplicitIntentWhenRemoteBudgetBlocks(t *testing.T) {
	s := &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "remote", ModelKey: "stable-alias"}},
			AllowedTools: []string{"web.search"},
		}},
		webProviders: map[string]managedProvider{
			"remote": {ID: "remote", Enabled: true, TokenBudgetPerDay: 1000},
		},
	}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	state := core.State{
		Request: core.Request{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"x"}],"tools":[]}`),
		},
		Model: core.ModelChoice{ProviderID: "remote", ModelKey: "stable-alias", UpstreamID: "upstream-model"},
	}
	_, err, handled := (inferenceForwarder{s: s}).forwardWithTools(context.Background(), state, &toolLoopTestProvider{}, req, "/v1/chat/completions", httptest.NewRecorder())
	coreErr, ok := err.(*core.Error)
	if !handled || !ok || coreErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("explicit tool request was not safely rejected: handled=%v err=%v", handled, err)
	}
}

type toolLoopRateLimitProvider struct{}

func (*toolLoopRateLimitProvider) ID() string { return "llamacpp" }
func (*toolLoopRateLimitProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Protocol: "openai-compatible", ChatCompletions: true}
}
func (*toolLoopRateLimitProvider) Forward(_ context.Context, call provider.InferenceCall) (core.Result, error) {
	body := []byte(`{"error":{"message":"upstream rate limited"}}`)
	call.Writer.Header().Set("Content-Type", "application/json")
	call.Writer.WriteHeader(http.StatusTooManyRequests)
	if _, err := call.Writer.Write(body); err != nil {
		return core.Result{}, err
	}
	return core.Result{
		Status: http.StatusTooManyRequests,
		UpstreamStatus: http.StatusTooManyRequests,
		Body: body,
	}, &provider.ProviderError{
		ProviderID: "llamacpp",
		Kind: provider.ErrorRateLimit,
		StatusCode: http.StatusTooManyRequests,
	}
}

func TestForwardWithToolsPreservesProviderNonSuccessResponse(t *testing.T) {
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name: "test.echo",
		Description: "Echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	if err := registry.Register(definition, toolLoopTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}},
			AllowedTools: []string{"test.echo"},
		}},
		toolRegistry: registry,
		toolRuntime: runtime,
	}
	recorder := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	state := core.State{
		Request: core.Request{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "active-model"},
	}
	state.ID = "request-2"
	s.callBegin(state.ID, &auth.Key{AllowedTools: []string{"test.echo"}})
	result, runErr, handled := (inferenceForwarder{s: s}).forwardWithTools(
		context.Background(), state, &toolLoopRateLimitProvider{}, req, "/v1/chat/completions", recorder,
	)
	if !handled || runErr == nil {
		t.Fatalf("provider error was not returned: handled=%v err=%v", handled, runErr)
	}
	if result.Status != http.StatusTooManyRequests || recorder.Code != http.StatusTooManyRequests ||
		!strings.Contains(recorder.Body.String(), "rate limited") {
		t.Fatalf("upstream error response was not written back: result=%+v status=%d body=%q", result, recorder.Code, recorder.Body.String())
	}
}

func TestMapToolCoordinatorTimeoutIsGatewayTimeout(t *testing.T) {
	mapped := mapToolCoordinatorError(&coordinator.ToolExecutionError{
		ToolName: "web.search",
		Cause: toolruntime.ErrExecutionTimeout,
	})
	coreErr, ok := mapped.(*core.Error)
	if !ok || coreErr.Status != http.StatusGatewayTimeout || coreErr.Code != "tool_execution_timeout" {
		t.Fatalf("tool timeout was not mapped to 504: %#v", mapped)
	}
}

func TestForwardWithToolsFallsBackForOrdinaryMultipleChoiceRequest(t *testing.T) {
	s := &Server{cfg: &config.Config{Tools: config.ToolsConfig{
		Enabled: true,
		AllowedModels: []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}},
		AllowedTools: []string{"web.search"},
	}}}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	state := core.State{
		Request: core.Request{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"x"}],"n":2}`),
		},
		Model: core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "active-model"},
	}
	_, err, handled := (inferenceForwarder{s: s}).forwardWithTools(context.Background(), state, &toolLoopTestProvider{}, req, "/v1/chat/completions", httptest.NewRecorder())
	if handled || err != nil {
		t.Fatalf("ordinary n>1 request did not preserve the existing path: handled=%v err=%v", handled, err)
	}
}


type toolLifecycleObserver struct {
	stages []core.Stage
}

func (o *toolLifecycleObserver) On(_ context.Context, event core.Event) {
	o.stages = append(o.stages, event.Stage)
}

func TestToolLoopRunsInsideSinglePipelineLifecycle(t *testing.T) {
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name: "test.echo",
		Description: "Echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	if err := registry.Register(definition, toolLoopTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	providerInstance := &toolLoopTestProvider{}
	providers := provider.NewRegistry()
	if err := providers.Register("llamacpp", func() provider.Provider { return providerInstance }); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}},
			AllowedTools: []string{"test.echo"},
		}},
		providers: providers,
		toolRegistry: registry,
		toolRuntime: runtime,
	}
	s.callBegin("tool-lifecycle-request", &auth.Key{AllowedTools: []string{"test.echo"}})
	observer := &toolLifecycleObserver{}
	recorder := httptest.NewRecorder()
	pipeline := &core.Pipeline{
		Resolve: func(context.Context, core.State) (core.ModelChoice, error) {
			return core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "active-model"}, nil
		},
		Forward: inferenceForwarder{s: s},
		Observe: observer,
	}
	result, err := pipeline.Run(context.Background(), core.Request{
		ID: "tool-lifecycle-request",
		Method: http.MethodPost,
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		Sink: &responseSink{w: recorder},
	})
	if err != nil {
		t.Fatalf("Pipeline.Run() error = %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("Pipeline.Run() status = %d, want 200", result.Status)
	}
	if providerInstance.calls != 2 {
		t.Fatalf("model rounds = %d, want 2", providerInstance.calls)
	}
	var upstreamStarts, finishes int
	for _, stage := range observer.stages {
		switch stage {
		case core.StageUpstreamStart:
			upstreamStarts++
		case core.StageFinish:
			finishes++
		}
	}
	if upstreamStarts != 1 || finishes != 1 {
		t.Fatalf("Pipeline lifecycle stages: upstream_start=%d finish=%d stages=%v", upstreamStarts, finishes, observer.stages)
	}
	if !strings.Contains(recorder.Body.String(), "done") ||
		strings.Contains(recorder.Body.String(), "tool_calls") ||
		strings.Contains(recorder.Body.String(), "echo") {
		t.Fatalf("client received intermediate tool content: %q", recorder.Body.String())
	}
}

type pipelineLifecycleCounter struct {
	parsed       int
	upstreamStart int
	finish       int
}

func (c *pipelineLifecycleCounter) On(_ context.Context, event core.Event) {
	switch event.Stage {
	case core.StageParsed:
		c.parsed++
	case core.StageUpstreamStart:
		c.upstreamStart++
	case core.StageFinish:
		c.finish++
	}
}

func TestToolLoopRunsInsideOnePipelineLifecycle(t *testing.T) {
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name: "test.echo",
		Description: "Echo",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	if err := registry.Register(definition, toolLoopTestExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	providerInstance := &toolLoopTestProvider{}
	providerRegistry := provider.NewRegistry()
	factoryCalls := 0
	if err := providerRegistry.Register("llamacpp", func() provider.Provider {
		factoryCalls++
		return providerInstance
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{
			Enabled: true,
			AllowedModels: []config.ToolModelAllowlist{{ProviderID: "llamacpp", ModelKey: "stable-key"}},
			AllowedTools: []string{"test.echo"},
		}},
		providers: providerRegistry,
		toolRegistry: registry,
		toolRuntime: runtime,
	}
	s.callBegin("pipeline-tool-loop", &auth.Key{AllowedTools: []string{"test.echo"}})
	observer := &pipelineLifecycleCounter{}
	pipe := &core.Pipeline{
		Resolve: func(context.Context, core.State) (core.ModelChoice, error) {
			return core.ModelChoice{ProviderID: "llamacpp", ModelKey: "stable-key", UpstreamID: "active-model"}, nil
		},
		Forward: inferenceForwarder{s: s},
		Observe: observer,
	}
	recorder := httptest.NewRecorder()
	sink := &responseSink{w: recorder}
	result, runErr := pipe.Run(context.Background(), core.Request{
		ID: "pipeline-tool-loop",
		Method: http.MethodPost,
		Path: "/v1/chat/completions",
		Body: []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"client.injected","parameters":{"type":"object"}}}]}`),
		Sink: sink,
		Started: time.Now(),
	})
	if runErr != nil {
		t.Fatalf("Pipeline.Run() error = %v", runErr)
	}
	if result.Status != http.StatusOK || recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), "done") ||
		strings.Contains(recorder.Body.String(), "tool_calls") {
		t.Fatalf("unexpected final response: result=%+v status=%d body=%q", result, recorder.Code, recorder.Body.String())
	}
	if observer.parsed != 1 || observer.upstreamStart != 1 || observer.finish != 1 {
		t.Fatalf("Pipeline lifecycle repeated: parsed=%d upstreamStart=%d finish=%d",
			observer.parsed, observer.upstreamStart, observer.finish)
	}
	if factoryCalls != 1 || providerInstance.calls != 2 {
		t.Fatalf("expected one Provider resolve and two model rounds: factory=%d calls=%d",
			factoryCalls, providerInstance.calls)
	}
	if len(providerInstance.modelIDs) != 2 ||
		providerInstance.modelIDs[0] != "active-model" ||
		providerInstance.modelIDs[1] != "active-model" {
		t.Fatalf("active ModelID changed between rounds: %v", providerInstance.modelIDs)
	}
}

func TestForwardWithToolsRejectsClientIntentWithoutKeyToolGrant(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(tools.Definition{Name:"test.echo",Description:"Echo",Parameters:json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, toolLoopTestExecutor{}); err != nil { t.Fatal(err) }
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil { t.Fatal(err) }
	s := &Server{cfg:&config.Config{Tools:config.ToolsConfig{Enabled:true,AllowedModels:[]config.ToolModelAllowlist{{ProviderID:"llamacpp",ModelKey:"stable-key"}},AllowedTools:[]string{"test.echo"}}},toolRegistry:registry,toolRuntime:runtime}
	s.callBegin("unauthorized-request", &auth.Key{})
	state := core.State{Request:core.Request{ID:"unauthorized-request",Path:"/v1/chat/completions",Body:[]byte(`{"messages":[{"role":"user","content":"x"}],"tools":[]}`)},Model:core.ModelChoice{ProviderID:"llamacpp",ModelKey:"stable-key"}}
	_, err, handled := (inferenceForwarder{s:s}).forwardWithTools(context.Background(),state,&toolLoopTestProvider{},httptest.NewRequest(http.MethodPost,"http://gateway/v1/chat/completions",nil),"/v1/chat/completions",httptest.NewRecorder())
	coreErr, ok := err.(*core.Error)
	if !handled || !ok || coreErr.Status != http.StatusForbidden || coreErr.Code != "tool_not_allowed" { t.Fatalf("expected fail-closed 403, handled=%v err=%#v",handled,err) }
}


func TestMapToolCallAuditEventUsesStableRuntimeErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err error
		status string
		code string
	}{
		{"authorization", toolruntime.ErrUnauthorized, "rejected", "tool_unauthorized"},
		{"authorization unavailable", toolruntime.ErrAuthorizationUnavailable, "rejected", "tool_authorization_unavailable"},
		{"concurrency", toolruntime.ErrConcurrencyLimit, "rejected", "tool_concurrency_limit"},
		{"timeout", toolruntime.ErrExecutionTimeout, "timed_out", "tool_timeout"},
		{"invalid result", toolruntime.ErrInvalidResult, "failed", "tool_result_invalid"},
		{"result too large", toolruntime.ErrResultTooLarge, "failed", "tool_result_too_large"},
		{"executor panic", toolruntime.ErrExecutorPanicked, "failed", "tool_executor_panicked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := mapToolCallAuditEvent(coordinator.ToolCallEvent{
				Seq: 1, Round: 1, ToolName: "web.search", Status: "failed", Cause: tc.err,
			})
			if event.Status != tc.status || event.ErrorCode != tc.code {
				t.Fatalf("mapped status/code = %s/%s, want %s/%s", event.Status, event.ErrorCode, tc.status, tc.code)
			}
		})
	}
}
