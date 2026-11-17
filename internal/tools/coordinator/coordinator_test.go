package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/tools"
)

type testModel struct {
	responses []ModelResponse
	requests  [][]byte
	err       error
}

func (m *testModel) Complete(_ context.Context, requestBody []byte) (ModelResponse, error) {
	m.requests = append(m.requests, append([]byte(nil), requestBody...))
	if len(m.responses) == 0 {
		return ModelResponse{}, errors.New("unexpected model call")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, m.err
}

type testExecutor struct {
	calls  []string
	result json.RawMessage
}

func (e *testExecutor) Execute(_ context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	e.calls = append(e.calls, name+":"+string(arguments))
	if e.result != nil {
		return append(json.RawMessage(nil), e.result...), nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func testCoordinator(t *testing.T, model *testModel, executor *testExecutor) *Coordinator {
	t.Helper()
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name:        "web.search",
		Description: "Search the web",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"],"additionalProperties":false}`),
	}
	if err := registry.Register(definition, executorAdapter{executor: executor}); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	coordinator, err := New(registry, executor, model)
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	return coordinator
}

func modelResponse(body string) ModelResponse {
	return ModelResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(body),
	}
}

func toolCallResponse(id, arguments string) string {
	return fmt.Sprintf(`{"id":"response-1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":%q,"type":"function","function":{"name":"web.search","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, id, arguments)
}

const finalResponse = `{"id":"response-final","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`

const requestBody = `{"model":"active-model","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"client.injected","parameters":{"type":"object"}}}]}`

func TestRunExecutesToolAndReturnsOnlyFinalModelResponse(t *testing.T) {
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"hello"}`)),
		modelResponse(finalResponse),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ModelRounds != 2 || result.ToolRounds != 1 || result.ToolCalls != 1 {
		t.Fatalf("unexpected counters: %+v", result)
	}
	if len(result.RoundUsage) != 1 {
		t.Fatalf("RoundUsage length = %d, want 1", len(result.RoundUsage))
	}
	if string(result.Response.Body) != finalResponse {
		t.Fatalf("final body = %s", result.Response.Body)
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}

	var firstRequest map[string]json.RawMessage
	if err := json.Unmarshal(model.requests[0], &firstRequest); err != nil {
		t.Fatal(err)
	}
	if string(firstRequest["parallel_tool_calls"]) != "false" {
		t.Fatalf("parallel tool calls must be disabled: %s", firstRequest["parallel_tool_calls"])
	}
	var sentTools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(firstRequest["tools"], &sentTools); err != nil {
		t.Fatal(err)
	}
	if len(sentTools) != 1 || sentTools[0].Function.Name != "web.search" {
		t.Fatalf("model received client-controlled tools: %s", firstRequest["tools"])
	}

	var secondRequest struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			Content    string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(model.requests[1], &secondRequest); err != nil {
		t.Fatal(err)
	}
	if len(secondRequest.Messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(secondRequest.Messages))
	}
	last := secondRequest.Messages[2]
	if last.Role != "tool" || last.ToolCallID != "call-1" || last.Content != `{"ok":true}` {
		t.Fatalf("unexpected tool result message: %+v", last)
	}
}

func TestRunRejectsDuplicateToolCallIDsBeforeExecutingRound(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"same","type":"function","function":{"name":"web.search","arguments":"{\"query\":\"a\"}"}},{"id":"same","type":"function","function":{"name":"web.search","arguments":"{\"query\":\"b\"}"}}]},"finish_reason":"tool_calls"}]}`
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor ran %d calls before whole-round validation", len(executor.calls))
	}
}

func TestRunRejectsUnknownToolBeforeExecution(t *testing.T) {
	body := strings.Replace(toolCallResponse("call-1", `{"query":"hello"}`), "web.search", "client.injected", 1)
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("Run() error = %v, want ErrUnknownTool", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunRejectsInvalidArgumentsBeforeExecutingRound(t *testing.T) {
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"ok","extra":true}`)),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunRejectsStreamingAndMultipleChoicesRequests(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"x"}],"stream":true}`,
		`{"messages":[{"role":"user","content":"x"}],"n":2}`,
	} {
		model := &testModel{}
		executor := &testExecutor{}
		coordinator := testCoordinator(t, model, executor)
		_, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
		if !errors.Is(err, ErrUnsupportedRequest) {
			t.Fatalf("Run(%s) error = %v, want ErrUnsupportedRequest", body, err)
		}
		if len(model.requests) != 0 {
			t.Fatal("unsupported request reached the model")
		}
	}
}

func TestRunStopsAfterConfiguredToolRoundBudget(t *testing.T) {
	responses := make([]ModelResponse, 0, MaxToolRounds+1)
	for i := 0; i < MaxToolRounds+1; i++ {
		responses = append(responses, modelResponse(toolCallResponse(fmt.Sprintf("call-%d", i), `{"query":"hello"}`)))
	}
	model := &testModel{responses: responses}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrToolBudgetExceeded", err)
	}
	if len(executor.calls) != MaxToolRounds {
		t.Fatalf("executor calls = %d, want %d", len(executor.calls), MaxToolRounds)
	}
	if len(model.requests) != MaxToolRounds+1 {
		t.Fatalf("model rounds = %d, want %d", len(model.requests), MaxToolRounds+1)
	}
}

func TestRunRejectsDuplicateJSONKeysInModelResponse(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[]},"finish_reason":"stop"}],"usage":{"total_tokens":1,"total_tokens":2}}`
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
}

func TestRunReturnsNonSuccessProviderResponseWithoutParsing(t *testing.T) {
	body := []byte("upstream error body")
	model := &testModel{responses: []ModelResponse{{
		StatusCode: http.StatusTooManyRequests,
		UpstreamStatus: http.StatusTooManyRequests,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: body,
	}}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Response.StatusCode != http.StatusTooManyRequests || string(result.Response.Body) != string(body) {
		t.Fatalf("provider response was not preserved: %+v", result.Response)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunRejectsToolResultBudgetOverflow(t *testing.T) {
	call := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"web.search","arguments":"{\"query\":\"hello\"}"}}`, id)
	}
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
		call("one") + `,` + call("two") + `,` + call("three") +
		`]},"finish_reason":"tool_calls"}]}`
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{result: json.RawMessage(`"` + strings.Repeat("x", 20*1024) + `"`)}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrToolResultBudget) {
		t.Fatalf("Run() error = %v, want ErrToolResultBudget", err)
	}
	if len(executor.calls) != 3 {
		t.Fatalf("executor calls = %d, want 3", len(executor.calls))
	}
}

func TestRunRejectsMoreThanThreeCallsInOneRound(t *testing.T) {
	call := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"web.search","arguments":"{\"query\":\"hello\"}"}}`, id)
	}
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
		call("one") + `,` + call("two") + `,` + call("three") + `,` + call("four") +
		`]},"finish_reason":"tool_calls"}]}`
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrToolBudgetExceeded", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunExecutesMultipleToolCallsSequentially(t *testing.T) {
	call := func(id, query string) string {
		arguments := fmt.Sprintf(`{"query":%q}`, query)
		return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"web.search","arguments":%q}}`, id, arguments)
	}
	body := `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
		call("first", "alpha") + `,` + call("second", "beta") +
		`]},"finish_reason":"tool_calls"}]}`
	model := &testModel{responses: []ModelResponse{
		modelResponse(body),
		modelResponse(finalResponse),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ToolCalls != 2 || len(executor.calls) != 2 {
		t.Fatalf("tool call count = %d, executor calls = %d", result.ToolCalls, len(executor.calls))
	}
	if !strings.Contains(executor.calls[0], `"query":"alpha"`) ||
		!strings.Contains(executor.calls[1], `"query":"beta"`) {
		t.Fatalf("tools were not executed in model order: %#v", executor.calls)
	}
	var nextRequest struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(model.requests[1], &nextRequest); err != nil {
		t.Fatal(err)
	}
	if len(nextRequest.Messages) != 4 || nextRequest.Messages[1].Role != "assistant" ||
		nextRequest.Messages[2].Role != "tool" || nextRequest.Messages[3].Role != "tool" {
		t.Fatalf("unexpected multi-tool conversation order: %+v", nextRequest.Messages)
	}
}

func TestRunHonorsCanceledRequestContext(t *testing.T) {
	model := &testModel{}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := coordinator.Run(ctx, []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(model.requests) != 0 || len(executor.calls) != 0 {
		t.Fatal("canceled request reached model or tool executor")
	}
}

func TestRunPreservesUpstreamHTTPErrorResponseAlongsideError(t *testing.T) {
	upstreamErr := errors.New("provider returned HTTP 429")
	body := []byte(`{"error":{"message":"rate limited"}}`)
	model := &testModel{
		responses: []ModelResponse{{
			StatusCode: http.StatusTooManyRequests,
			UpstreamStatus: http.StatusTooManyRequests,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: body,
		}},
		err: upstreamErr,
	}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("Run() error = %v, want original upstream error", err)
	}
	if result.ModelRounds != 1 || result.Response.StatusCode != http.StatusTooManyRequests ||
		result.Response.UpstreamStatus != http.StatusTooManyRequests ||
		string(result.Response.Body) != string(body) {
		t.Fatalf("upstream response was lost: %+v", result.Response)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunReturnsPartialCountersWhenToolExecutionFails(t *testing.T) {
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"hello"}`)),
	}}
	executor := &failingTestExecutor{err: errors.New("private executor failure")}
	coordinator := testCoordinatorWithExecutor(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(err, ErrToolExecutionFailed) {
		t.Fatalf("Run() error = %v, want ErrToolExecutionFailed", err)
	}
	if result.ModelRounds != 1 || result.ToolRounds != 1 || result.ToolCalls != 0 {
		t.Fatalf("partial counters were lost: %+v", result)
	}
}

type failingTestExecutor struct {
	err error
}

func (e *failingTestExecutor) Execute(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, e.err
}

func testCoordinatorWithExecutor(t *testing.T, model *testModel, executor ToolExecutor) *Coordinator {
	t.Helper()
	registry := tools.NewRegistry()
	definition := tools.Definition{
		Name: "web.search",
		Description: "Search the web",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"],"additionalProperties":false}`),
	}
	if err := registry.Register(definition, executorAdapter{executor: executor}); err != nil {
		t.Fatalf("register tool: %v", err)
	}
	coordinator, err := New(registry, executor, model)
	if err != nil {
		t.Fatalf("create coordinator: %v", err)
	}
	return coordinator
}

type executorAdapter struct {
	executor ToolExecutor
}

func (a executorAdapter) Execute(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	return a.executor.Execute(ctx, "web.search", arguments)
}

func TestRunDoesNotExecuteToolsWhenToolChoiceIsNone(t *testing.T) {
	body := strings.TrimSuffix(requestBody, "}") + `,"tool_choice":"none"}`
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"hello"}`)),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("tool executed despite tool_choice=none: %d calls", len(executor.calls))
	}
}

func TestRunRequiresToolCallWhenToolChoiceIsRequired(t *testing.T) {
	body := strings.TrimSuffix(requestBody, "}") + `,"tool_choice":"required"}`
	model := &testModel{responses: []ModelResponse{modelResponse(finalResponse)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunEnforcesNamedToolChoice(t *testing.T) {
	body := strings.TrimSuffix(requestBody, "}") +
		`,"tool_choice":{"type":"function","function":{"name":"web.search"}}}`
	model := &testModel{responses: []ModelResponse{
		modelResponse(strings.Replace(toolCallResponse("call-1", `{"query":"hello"}`), "web.search", "other.tool", 1)),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	_, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
	if !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("Run() error = %v, want ErrInvalidModelResponse", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}

func TestRunResetsRequiredToolChoiceAfterFirstToolRound(t *testing.T) {
	body := strings.TrimSuffix(requestBody, "}") + `,"tool_choice":"required"}`
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"hello"}`)),
		modelResponse(finalResponse),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ToolCalls != 1 || result.ModelRounds != 2 {
		t.Fatalf("unexpected result counters: %+v", result)
	}
	var first, second map[string]json.RawMessage
	if err := json.Unmarshal(model.requests[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(model.requests[1], &second); err != nil {
		t.Fatal(err)
	}
	if string(first["tool_choice"]) != `"required"` {
		t.Fatalf("first tool_choice = %s, want required", first["tool_choice"])
	}
	if string(second["tool_choice"]) != `"auto"` {
		t.Fatalf("follow-up tool_choice = %s, want auto", second["tool_choice"])
	}
}

func TestRunRejectsLegacyFunctionCallingFields(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"x"}],"functions":[]}`,
		`{"messages":[{"role":"user","content":"x"}],"function_call":"auto"}`,
	} {
		model := &testModel{}
		executor := &testExecutor{}
		coordinator := testCoordinator(t, model, executor)
		_, err := coordinator.Run(context.Background(), []byte(body), []string{"web.search"})
		if !errors.Is(err, ErrUnsupportedRequest) {
			t.Fatalf("Run(%s) error = %v, want ErrUnsupportedRequest", body, err)
		}
		if len(model.requests) != 0 {
			t.Fatal("legacy function calling request reached the model")
		}
	}
}

type errorResponseModel struct {
	response ModelResponse
	err      error
}

func (m errorResponseModel) Complete(context.Context, []byte) (ModelResponse, error) {
	return m.response, m.err
}

func TestRunPreservesNonSuccessProviderResponseReturnedWithError(t *testing.T) {
	body := []byte(`{"error":{"message":"upstream rate limited"}}`)
	upstreamErr := errors.New("provider returned HTTP 429")
	registry := tools.NewRegistry()
	executor := &testExecutor{}
	definition := tools.Definition{
		Name: "web.search",
		Description: "Search the web",
		Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
	if err := registry.Register(definition, executorAdapter{executor: executor}); err != nil {
		t.Fatal(err)
	}
	coordinator, err := New(registry, executor, errorResponseModel{
		response: ModelResponse{
			StatusCode: http.StatusTooManyRequests,
			UpstreamStatus: http.StatusTooManyRequests,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: body,
		},
		err: upstreamErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := coordinator.Run(context.Background(), []byte(requestBody), []string{"web.search"})
	if !errors.Is(runErr, upstreamErr) {
		t.Fatalf("Run() error = %v, want original Provider error", runErr)
	}
	if result.Response.StatusCode != http.StatusTooManyRequests ||
		result.Response.UpstreamStatus != http.StatusTooManyRequests ||
		string(result.Response.Body) != string(body) {
		t.Fatalf("upstream error response was lost: %+v", result.Response)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.calls))
	}
}


func TestRunWithObserverRecordsSuccessfulToolCallWithoutSensitivePayload(t *testing.T) {
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("private-call-id", `{"query":"private search phrase"}`)),
		modelResponse(finalResponse),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)
	var events []ToolCallEvent

	_, err := coordinator.RunWithObserver(context.Background(), []byte(requestBody), []string{"web.search"}, func(event ToolCallEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("RunWithObserver() error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Seq != 1 || event.Round != 1 || event.ToolName != "web.search" ||
		event.Status != "succeeded" || event.ErrorCode != "" {
		t.Fatalf("unexpected event: %+v", event)
	}
	if event.ResultBytes != int64(len(`{"ok":true}`)) {
		t.Fatalf("result_bytes = %d", event.ResultBytes)
	}
	if event.Cause != nil {
		t.Fatalf("successful event contains error cause: %v", event.Cause)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-call-id", "private search phrase", "arguments"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized event contains sensitive content %q: %s", secret, encoded)
		}
	}
}

func TestRunWithObserverRecordsRejectedUnknownTool(t *testing.T) {
	body := strings.Replace(toolCallResponse("call-1", `{"query":"hello"}`), "web.search", "unknown.private.tool", 1)
	model := &testModel{responses: []ModelResponse{modelResponse(body)}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)
	var events []ToolCallEvent

	_, err := coordinator.RunWithObserver(context.Background(), []byte(requestBody), []string{"web.search"}, func(event ToolCallEvent) {
		events = append(events, event)
	})
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("RunWithObserver() error = %v, want ErrUnknownTool", err)
	}
	if len(executor.calls) != 0 || len(events) != 1 {
		t.Fatalf("executor calls=%d events=%d", len(executor.calls), len(events))
	}
	if events[0].Seq != 1 || events[0].Round != 1 ||
		events[0].Status != "rejected" || events[0].ErrorCode != "unknown_tool" {
		t.Fatalf("unexpected rejection event: %+v", events[0])
	}
}

func TestRunWithObserverClassifiesExecutionTimeout(t *testing.T) {
	model := &testModel{responses: []ModelResponse{modelResponse(toolCallResponse("call-1", `{"query":"hello"}`))}}
	executor := &failingTestExecutor{err: context.DeadlineExceeded}
	coordinator := testCoordinatorWithExecutor(t, model, executor)
	var events []ToolCallEvent

	_, err := coordinator.RunWithObserver(context.Background(), []byte(requestBody), []string{"web.search"}, func(event ToolCallEvent) {
		events = append(events, event)
	})
	if !errors.Is(err, ErrToolExecutionFailed) {
		t.Fatalf("RunWithObserver() error = %v, want ErrToolExecutionFailed", err)
	}
	if len(events) != 1 || events[0].Status != "timed_out" ||
		events[0].ErrorCode != "tool_timeout" || !errors.Is(events[0].Cause, context.DeadlineExceeded) {
		t.Fatalf("unexpected timeout event: %+v", events)
	}
}

func TestRunWithObserverPanicDoesNotChangeToolResult(t *testing.T) {
	model := &testModel{responses: []ModelResponse{
		modelResponse(toolCallResponse("call-1", `{"query":"hello"}`)),
		modelResponse(finalResponse),
	}}
	executor := &testExecutor{}
	coordinator := testCoordinator(t, model, executor)

	result, err := coordinator.RunWithObserver(context.Background(), []byte(requestBody), []string{"web.search"}, func(ToolCallEvent) {
		panic("observer must not affect inference")
	})
	if err != nil {
		t.Fatalf("RunWithObserver() error = %v", err)
	}
	if result.ToolCalls != 1 || string(result.Response.Body) != finalResponse {
		t.Fatalf("observer changed inference result: %+v", result)
	}
}
