// Package coordinator 管理单次 Chat Completions 请求中的模型—工具多轮交互。
//
// Coordinator 不依赖 Provider 或具体工具模块。模型调用与单次工具执行均通过接口注入；
// Provider 选择、工具授权集合和请求生命周期由外层 Server 负责。
package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"unicode/utf8"

	"github.com/whl/llm-gateway/internal/tools"
)

const (
	MaxToolRounds          = 3
	MaxToolCallsPerRound   = 3
	MaxToolResultBytes     = 48 * 1024
	MaxModelResponseBytes  = 32 * 1024 * 1024
	MaxToolCallIDBytes     = 256
	MaxStrictJSONDepth     = 32
)

var (
	ErrCoordinatorUnavailable = errors.New("tool coordinator is unavailable")
	ErrInvalidContext         = errors.New("tool coordinator context is nil")
	ErrInvalidRequest         = errors.New("tool coordinator request is invalid")
	ErrUnsupportedRequest     = errors.New("tool coordinator request is not supported")
	ErrNoAllowedTools         = errors.New("no server-authorized tools are available")
	ErrInvalidModelResponse   = errors.New("model returned an invalid tool-calling response")
	ErrUnknownTool            = errors.New("model requested an unavailable tool")
	ErrToolBudgetExceeded     = errors.New("tool request budget exceeded")
	ErrToolExecutionFailed    = errors.New("tool execution failed")
	ErrModelResponseTooLarge  = errors.New("model response exceeds the configured size limit")
	ErrToolResultBudget       = errors.New("cumulative tool results exceed the request limit")
)

// ModelResponse 是一次非流式模型调用的完整响应。Body 不得引用可复用的上游读缓冲区。
type ModelResponse struct {
	StatusCode     int
	UpstreamStatus int
	Header         http.Header
	Body           []byte
}

// ModelCaller 只负责一次非流式 Chat Completions 请求，不负责工具解析或执行。
type ModelCaller interface {
	Complete(ctx context.Context, requestBody []byte) (ModelResponse, error)
}

// ToolExecutor 与 tools/runtime.Runtime 的 Execute 方法兼容。
type ToolExecutor interface {
	Execute(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error)
}

// Result 保留最终模型响应和各轮模型 usage 证据，供 Server 与后续审计层使用。
type Result struct {
	Response    ModelResponse
	ModelRounds int
	ToolRounds  int
	ToolCalls   int
	RoundUsage  []json.RawMessage
}

// ToolExecutionError 隐藏工具实现的原始错误文本，同时保留 Unwrap 供内部诊断。
type ToolExecutionError struct {
	ToolName string
	Cause    error
}

func (e *ToolExecutionError) Error() string {
	if e == nil {
		return ErrToolExecutionFailed.Error()
	}
	return fmt.Sprintf("%s: %s", ErrToolExecutionFailed, e.ToolName)
}

func (e *ToolExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ToolExecutionError) Is(target error) bool {
	return target == ErrToolExecutionFailed
}

// Coordinator 协调同一请求中的模型调用和工具执行。
type Coordinator struct {
	registry *tools.Registry
	executor ToolExecutor
	model    ModelCaller
}

// New 创建 Coordinator。授权工具名称由每次 Run 的调用方提供，避免把请求级权限缓存到实例。
func New(registry *tools.Registry, executor ToolExecutor, model ModelCaller) (*Coordinator, error) {
	if registry == nil || isNil(executor) || isNil(model) {
		return nil, ErrCoordinatorUnavailable
	}
	return &Coordinator{registry: registry, executor: executor, model: model}, nil
}

// Run 使用服务端提供的允许工具名称执行一次非流式 Chat Completions 请求。
// 请求体中的客户端 tools 会被服务端 Registry 定义替换，不能注册或覆盖工具。
func (c *Coordinator) Run(ctx context.Context, requestBody []byte, allowedToolNames []string) (Result, error) {
	result := Result{}
	if c == nil || c.registry == nil || isNil(c.executor) || isNil(c.model) {
		return result, ErrCoordinatorUnavailable
	}
	if ctx == nil {
		return result, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(allowedToolNames) == 0 {
		return result, ErrNoAllowedTools
	}

	request, messages, allowed, err := c.prepareRequest(requestBody, allowedToolNames)
	if err != nil {
		return result, err
	}

	seenCallIDs := make(map[string]struct{})
	totalToolResultBytes := 0

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		encodedRequest, err := json.Marshal(request)
		if err != nil {
			return result, fmt.Errorf("%w: encode model request", ErrInvalidRequest)
		}
		modelResponse, err := c.model.Complete(ctx, encodedRequest)
		if err != nil {
			// Provider 收到上游非 2xx 响应时可能同时返回响应体与错误；
			// 保留完整上游响应供 Server 按既有错误语义处理。传输错误没有上游状态，不保留半截响应。
			if modelResponse.StatusCode >= http.StatusMultipleChoices &&
				modelResponse.StatusCode <= 599 &&
				modelResponse.UpstreamStatus == modelResponse.StatusCode &&
				len(modelResponse.Body) > 0 {
				result.Response = cloneModelResponse(modelResponse)
			}
			return result, err
		}
		result.ModelRounds++
		if modelResponse.StatusCode < 100 || modelResponse.StatusCode > 599 {
			return result, fmt.Errorf("%w: invalid model HTTP status", ErrInvalidModelResponse)
		}
		if len(modelResponse.Body) > MaxModelResponseBytes {
			return result, ErrModelResponseTooLarge
		}
		if modelResponse.StatusCode < http.StatusOK || modelResponse.StatusCode >= http.StatusMultipleChoices {
			result.Response = cloneModelResponse(modelResponse)
			return result, nil
		}

		round, err := parseToolRound(modelResponse.Body)
		if err != nil {
			return result, err
		}
		if len(round.usage) > 0 && string(round.usage) != "null" {
			result.RoundUsage = append(result.RoundUsage, append(json.RawMessage(nil), round.usage...))
		}
		if round.finishReason != "tool_calls" {
			if len(round.calls) != 0 {
				return result, ErrInvalidModelResponse
			}
			result.Response = cloneModelResponse(modelResponse)
			return result, nil
		}
		if len(round.calls) == 0 {
			return result, ErrInvalidModelResponse
		}
		if result.ToolRounds >= MaxToolRounds {
			return result, ErrToolBudgetExceeded
		}
		if len(round.calls) > MaxToolCallsPerRound {
			return result, ErrToolBudgetExceeded
		}

		// 整轮先完成协议、工具存在性和参数校验，避免明显无效的后续调用导致前序工具先执行。
		prepared := make([]preparedCall, 0, len(round.calls))
		roundCallIDs := make(map[string]struct{}, len(round.calls))
		for _, call := range round.calls {
			if call.ID == "" || len(call.ID) > MaxToolCallIDBytes || !utf8.ValidString(call.ID) {
				return result, ErrInvalidModelResponse
			}
			if _, exists := seenCallIDs[call.ID]; exists {
				return result, ErrInvalidModelResponse
			}
			if _, exists := roundCallIDs[call.ID]; exists {
				return result, ErrInvalidModelResponse
			}
			roundCallIDs[call.ID] = struct{}{}
			if call.Type != "function" || call.Function.Name == "" {
				return result, ErrInvalidModelResponse
			}
			if _, ok := allowed[call.Function.Name]; !ok {
				return result, fmt.Errorf("%w: %s", ErrUnknownTool, call.Function.Name)
			}
			definition, _, lookupErr := c.registry.Lookup(call.Function.Name)
			if lookupErr != nil {
				return result, fmt.Errorf("%w: %s", ErrUnknownTool, call.Function.Name)
			}
			arguments := json.RawMessage(call.Function.Arguments)
			if err := validateStrictJSON(arguments, MaxStrictJSONDepth); err != nil {
				return result, fmt.Errorf("%w: invalid arguments JSON", ErrInvalidModelResponse)
			}
			if err := tools.ValidateArguments(definition.Parameters, arguments); err != nil {
				return result, fmt.Errorf("%w: invalid tool arguments", ErrInvalidModelResponse)
			}
			prepared = append(prepared, preparedCall{id: call.ID, name: call.Function.Name, arguments: arguments})
		}

		messages = append(messages, append(json.RawMessage(nil), round.message...))
		for _, call := range prepared {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			output, executeErr := c.executor.Execute(ctx, call.name, append(json.RawMessage(nil), call.arguments...))
			if executeErr != nil {
				return result, &ToolExecutionError{ToolName: call.name, Cause: executeErr}
			}
			if len(output) == 0 || !json.Valid(output) || !utf8.Valid(output) {
				return result, fmt.Errorf("%w: tool %s returned invalid JSON", ErrToolExecutionFailed, call.name)
			}
			if len(output) > MaxToolResultBytes-totalToolResultBytes {
				return result, ErrToolResultBudget
			}
			totalToolResultBytes += len(output)
			toolMessage, marshalErr := json.Marshal(struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				Content    string `json:"content"`
			}{Role: "tool", ToolCallID: call.id, Content: string(output)})
			if marshalErr != nil {
				return result, fmt.Errorf("%w: encode tool result", ErrToolExecutionFailed)
			}
			messages = append(messages, toolMessage)
			seenCallIDs[call.id] = struct{}{}
			result.ToolCalls++
		}
		result.ToolRounds++
		request["messages"], err = json.Marshal(messages)
		if err != nil {
			return result, fmt.Errorf("%w: encode conversation messages", ErrInvalidRequest)
		}
	}
}

type preparedCall struct {
	id        string
	name      string
	arguments json.RawMessage
}

type parsedRound struct {
	message      json.RawMessage
	finishReason string
	calls        []modelToolCall
	usage        json.RawMessage
}

type modelToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (c *Coordinator) prepareRequest(body []byte, allowedNames []string) (map[string]json.RawMessage, []json.RawMessage, map[string]struct{}, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return nil, nil, nil, ErrInvalidRequest
	}
	if err := validateStrictJSON(body, MaxStrictJSONDepth); err != nil {
		return nil, nil, nil, ErrInvalidRequest
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return nil, nil, nil, ErrInvalidRequest
	}
	if raw := request["stream"]; len(raw) > 0 {
		var stream bool
		if json.Unmarshal(raw, &stream) != nil {
			return nil, nil, nil, ErrInvalidRequest
		}
		if stream {
			return nil, nil, nil, ErrUnsupportedRequest
		}
	}
	if raw := request["n"]; len(raw) > 0 {
		var n int
		if json.Unmarshal(raw, &n) != nil || n != 1 {
			return nil, nil, nil, ErrUnsupportedRequest
		}
	}
	rawMessages, ok := request["messages"]
	if !ok {
		return nil, nil, nil, ErrInvalidRequest
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil || len(messages) == 0 {
		return nil, nil, nil, ErrInvalidRequest
	}
	for _, message := range messages {
		if err := validateMessageObject(message); err != nil {
			return nil, nil, nil, ErrInvalidRequest
		}
	}

	orderedNames := append([]string(nil), allowedNames...)
	sort.Strings(orderedNames)
	allowed := make(map[string]struct{}, len(orderedNames))
	definitions := make([]tools.Definition, 0, len(orderedNames))
	for _, name := range orderedNames {
		if name == "" {
			return nil, nil, nil, ErrUnknownTool
		}
		if _, duplicate := allowed[name]; duplicate {
			return nil, nil, nil, ErrInvalidRequest
		}
		definition, _, err := c.registry.Lookup(name)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %s", ErrUnknownTool, name)
		}
		allowed[name] = struct{}{}
		definitions = append(definitions, definition)
	}
	if err := validateToolChoice(request["tool_choice"], allowed); err != nil {
		return nil, nil, nil, err
	}

	toolDefinitions := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		toolDefinitions = append(toolDefinitions, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": definition.Name,
				"description": definition.Description,
				"parameters": json.RawMessage(definition.Parameters),
			},
		})
	}
	encodedTools, err := json.Marshal(toolDefinitions)
	if err != nil {
		return nil, nil, nil, ErrCoordinatorUnavailable
	}
	request["tools"] = encodedTools
	request["messages"] = rawMessages
	return request, messages, allowed, nil
}

func validateToolChoice(raw json.RawMessage, allowed map[string]struct{}) error {
	if len(raw) == 0 {
		return nil
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto", "none", "required":
			return nil
		default:
			return ErrUnsupportedRequest
		}
	}
	var object struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &object) != nil || object.Type != "function" || object.Function.Name == "" {
		return ErrUnsupportedRequest
	}
	if _, ok := allowed[object.Function.Name]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownTool, object.Function.Name)
	}
	return nil
}

func parseToolRound(body []byte) (parsedRound, error) {
	if len(body) == 0 || !utf8.Valid(body) {
		return parsedRound{}, ErrInvalidModelResponse
	}
	if err := validateStrictJSON(body, MaxStrictJSONDepth); err != nil {
		return parsedRound{}, ErrInvalidModelResponse
	}
	var response struct {
		Choices []struct {
			Message      json.RawMessage `json:"message"`
			FinishReason string          `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Choices) != 1 {
		return parsedRound{}, ErrInvalidModelResponse
	}
	choice := response.Choices[0]
	if choice.FinishReason == "" || len(choice.Message) == 0 {
		return parsedRound{}, ErrInvalidModelResponse
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(choice.Message, &message); err != nil || message == nil {
		return parsedRound{}, ErrInvalidModelResponse
	}
	var role string
	if json.Unmarshal(message["role"], &role) != nil || role != "assistant" {
		return parsedRound{}, ErrInvalidModelResponse
	}
	round := parsedRound{
		message: append(json.RawMessage(nil), choice.Message...),
		finishReason: choice.FinishReason,
		usage: append(json.RawMessage(nil), response.Usage...),
	}
	if rawCalls, exists := message["tool_calls"]; exists {
		if err := json.Unmarshal(rawCalls, &round.calls); err != nil {
			return parsedRound{}, ErrInvalidModelResponse
		}
	}
	if choice.FinishReason == "tool_calls" && len(round.calls) == 0 {
		return parsedRound{}, ErrInvalidModelResponse
	}
	if choice.FinishReason != "tool_calls" && len(round.calls) != 0 {
		return parsedRound{}, ErrInvalidModelResponse
	}
	return round, nil
}

func validateMessageObject(raw json.RawMessage) error {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(raw, &message); err != nil || message == nil {
		return ErrInvalidRequest
	}
	var role string
	if json.Unmarshal(message["role"], &role) != nil || role == "" {
		return ErrInvalidRequest
	}
	return nil
}

// validateStrictJSON rejects duplicate object keys, invalid UTF-8, trailing values and excessive nesting.
func validateStrictJSON(raw []byte, maxDepth int) error {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return errors.New("invalid JSON bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, 0, maxDepth); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder, depth+1, maxDepth); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func cloneModelResponse(response ModelResponse) ModelResponse {
	response.Header = response.Header.Clone()
	response.Body = append([]byte(nil), response.Body...)
	return response
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
