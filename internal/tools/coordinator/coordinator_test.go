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
}

func (m *testModel) Complete(_ context.Context, requestBody []byte) (ModelResponse, error) {
	m.requests = append(m.requests, append([]byte(nil), requestBody...))
	if len(m.responses) == 0 {
		return ModelResponse{}, errors.New("unexpected model call")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
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
	if err := registry.Register(definition, executor); err != nil {
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
		return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"web.search","arguments":"{\"query\":%q}"}}`, id, query)
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
