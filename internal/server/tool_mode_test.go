package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
