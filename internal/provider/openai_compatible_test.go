package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/core"
)

func testRemoteProvider(t *testing.T, server *httptest.Server, opts OpenAICompatibleConfig) *OpenAICompatibleProvider {
	t.Helper()
	opts.ID = "test-remote"
	opts.BaseURL = server.URL + "/v1"
	opts.AllowInsecureHTTP = true
	opts.AllowPrivateNetwork = true
	p, err := NewOpenAICompatibleProvider(opts)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}
	return p
}

func TestNewOpenAICompatibleProviderRejectsUnsafeURLAndAuthHeader(t *testing.T) {
	cases := []OpenAICompatibleConfig{
		{ID: "unsafe", BaseURL: "http://127.0.0.1:8080/v1"},
		{ID: "unsafe", BaseURL: "https://user:password@example.com/v1"},
		{ID: "unsafe", BaseURL: "https://example.com/v1?token=private"},
		{ID: "unsafe", BaseURL: "https://127.0.0.1/v1"},
		{ID: "unsafe", BaseURL: "https://example.com/v1", APIKeyHeader: "X-Private-Header"},
		{ID: "bad id", BaseURL: "https://example.com/v1"},
	}
	for i, cfg := range cases {
		if _, err := NewOpenAICompatibleProvider(cfg); err == nil {
			t.Errorf("case %d: expected config rejection", i)
		}
	}
}

func TestOpenAICompatibleProviderRewritesModelAndUsesOwnCredential(t *testing.T) {
	var gotAuthorization, gotPath, gotQuery, gotModel, gotMessage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		gotModel, _ = payload["model"].(string)
		if messages, ok := payload["messages"].([]interface{}); ok && len(messages) > 0 {
			if first, ok := messages[0].(map[string]interface{}); ok {
				gotMessage, _ = first["content"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"id\":\"ok\"}"))
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "sk-provider-secret"})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer gateway-caller-key")
	request.Header.Set("X-API-Key", "gateway-caller-key")
	res, err := p.Forward(context.Background(), InferenceCall{
		Writer: recorder, Request: request, Path: "/v1/chat/completions?trace_id=abc",
		Body: []byte("{\"model\":\"client-model\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"),
		ModelID: "mapped-provider-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuthorization != "Bearer sk-provider-secret" {
		t.Fatalf("upstream Authorization = %q", gotAuthorization)
	}
	if gotPath != "/v1/chat/completions" || gotQuery != "trace_id=abc" {
		t.Fatalf("upstream path/query = %q/%q", gotPath, gotQuery)
	}
	if gotModel != "mapped-provider-model" || gotMessage != "hi" {
		t.Fatalf("upstream model/message = %q/%q", gotModel, gotMessage)
	}
	if res.Status != http.StatusOK || !strings.Contains(recorder.Body.String(), "\"id\":\"ok\"") {
		t.Fatalf("result/body = %+v/%q", res, recorder.Body.String())
	}
}

func TestOpenAICompatibleProviderSupportsAPIKeyHeaderMode(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("api-key")
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "secret-key", APIKeyHeader: "api-key"})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err := p.Forward(context.Background(), InferenceCall{Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" {
		t.Fatalf("api-key header = %q", gotKey)
	}
}

func TestOpenAICompatibleProviderForwardsResponsesJSONAndSSE(t *testing.T) {
	const upstreamJSON = "{\"id\":\"resp_json\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4.1\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello json\"}]}]}"
	const upstreamSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"gpt-4.1\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello from Responses\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-4.1\"}}\n\n"

	type upstreamCapture struct {
		path          string
		query         string
		model         string
		stream        bool
		accept        string
		openAIBeta    string
		authorization string
		gatewayKey    string
	}
	captures := make(chan upstreamCapture, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request body", http.StatusBadRequest)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}
		var modelID string
		var streaming bool
		if err := json.Unmarshal(payload["model"], &modelID); err != nil {
			http.Error(w, "model missing", http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(payload["stream"], &streaming); err != nil {
			http.Error(w, "stream missing", http.StatusBadRequest)
			return
		}
		captures <- upstreamCapture{
			path: r.URL.Path, query: r.URL.RawQuery, model: modelID, stream: streaming,
			accept: r.Header.Get("Accept"), openAIBeta: r.Header.Get("OpenAI-Beta"),
			authorization: r.Header.Get("Authorization"), gatewayKey: r.Header.Get("X-API-Key"),
		}
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Request-Id", "req_responses_stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, upstreamSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Request-Id", "req_responses_json")
		_, _ = io.WriteString(w, upstreamJSON)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{
		APIKey: "provider-secret", APIKeyHeader: "authorization",
	})
	for _, tc := range []struct {
		name       string
		stream     bool
		query      string
		wantID     string
		wantAccept string
	}{
		{name: "JSON", stream: false, query: "trace_id=responses-json", wantID: "req_responses_json", wantAccept: "application/json"},
		{name: "SSE", stream: true, query: "trace_id=responses-stream", wantID: "req_responses_stream", wantAccept: "text/event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]interface{}{
				"model": "client-model-must-be-overridden",
				"stream": tc.stream,
				"input": "Reply with a brief acknowledgement.",
			})
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			request.Header.Set("OpenAI-Beta", "responses=codex-test")
			request.Header.Set("Authorization", "Bearer gateway-caller-secret")
			request.Header.Set("X-API-Key", "gateway-caller-secret")
			writer := httptest.NewRecorder()
			result, err := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: request,
				Path: "/v1/responses?" + tc.query,
				Body: body, ModelID: "gpt-4.1", Stream: tc.stream,
			})
			if err != nil {
				t.Fatalf("Provider Forward: %v", err)
			}
			if result.Status != http.StatusOK || writer.Code != http.StatusOK {
				t.Fatalf("result/writer status = %d/%d, want 200", result.Status, writer.Code)
			}
			capture := <-captures
			if capture.path != "/v1/responses" || capture.query != tc.query {
				t.Errorf("upstream path/query = %q/%q, want /v1/responses?%s", capture.path, capture.query, tc.query)
			}
			if capture.model != "gpt-4.1" || capture.stream != tc.stream {
				t.Errorf("upstream model/stream = %q/%v, want gpt-4.1/%v", capture.model, capture.stream, tc.stream)
			}
			if capture.accept != tc.wantAccept {
				t.Errorf("upstream Accept = %q, want %q", capture.accept, tc.wantAccept)
			}
			if capture.openAIBeta != "responses=codex-test" {
				t.Errorf("OpenAI-Beta = %q, want passthrough", capture.openAIBeta)
			}
			if capture.authorization != "Bearer provider-secret" || capture.gatewayKey != "" {
				t.Errorf("upstream credentials = Authorization:%q X-API-Key:%q; caller credentials must not leak", capture.authorization, capture.gatewayKey)
			}
			if writer.Header().Get("Request-Id") != tc.wantID {
				t.Errorf("Request-Id = %q, want %q", writer.Header().Get("Request-Id"), tc.wantID)
			}

			if tc.stream {
				if !strings.HasPrefix(writer.Header().Get("Content-Type"), "text/event-stream") {
					t.Errorf("SSE Content-Type = %q", writer.Header().Get("Content-Type"))
				}
				if writer.Body.String() != upstreamSSE {
					t.Fatalf("Responses SSE passthrough body mismatch: got %q, want %q", writer.Body.String(), upstreamSSE)
				}
				if !strings.Contains(writer.Body.String(), "hello from Responses") ||
					!strings.Contains(writer.Body.String(), "event: response.completed") {
					t.Fatalf("Responses SSE lifecycle incomplete: %q", writer.Body.String())
				}
				return
			}

			if writer.Body.String() != upstreamJSON {
				t.Fatalf("Responses JSON body changed: got %q want %q", writer.Body.String(), upstreamJSON)
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
				t.Fatalf("Responses JSON is invalid: %v", err)
			}
			var responseID, responseModel, responseStatus string
			var output []json.RawMessage
			_ = json.Unmarshal(response["id"], &responseID)
			_ = json.Unmarshal(response["model"], &responseModel)
			_ = json.Unmarshal(response["status"], &responseStatus)
			_ = json.Unmarshal(response["output"], &output)
			if responseID != "resp_json" || responseModel != "gpt-4.1" ||
				responseStatus != "completed" || len(output) == 0 {
				t.Fatalf("unexpected Responses JSON fields: id=%q model=%q status=%q output_count=%d", responseID, responseModel, responseStatus, len(output))
			}
		})
	}
}

func TestOpenAICompatibleProviderForwardsAnthropicMessagesHeaders(t *testing.T) {
	var gotVersion, gotBeta, gotFutureHeader, gotWorkspace, gotKey, gotAuthorization, gotModel, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("Anthropic-Version")
		gotBeta = r.Header.Get("Anthropic-Beta")
		gotFutureHeader = r.Header.Get("Anthropic-Future-Capability")
		gotWorkspace = r.Header.Get("Anthropic-Workspace-Id")
		gotKey = r.Header.Get("X-API-Key")
		gotAuthorization = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		gotModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_test","type":"message"}`)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{
		APIKey: "provider-secret", APIKeyHeader: "x-api-key",
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	request.Header.Set("Anthropic-Future-Capability", "future-value")
	request.Header.Set("Anthropic-Workspace-Id", "workspace_test")
	request.Header.Set("X-API-Key", "gateway-caller-secret")
	request.Header.Set("Authorization", "Bearer gateway-caller-secret")
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request,
		Path: "/v1/messages?trace_id=anthropic-test",
		Body: []byte(`{"model":"client-model","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`),
		ModelID: "claude-model-mapped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != "2023-06-01" || gotBeta != "prompt-caching-2024-07-31" ||
		gotFutureHeader != "future-value" || gotWorkspace != "workspace_test" {
		t.Fatalf("Anthropic protocol headers = version %q, beta %q, future %q, workspace %q", gotVersion, gotBeta, gotFutureHeader, gotWorkspace)
	}
	if gotKey != "provider-secret" || gotAuthorization != "" {
		t.Fatalf("upstream credentials were not isolated: x-api-key=%q authorization=%q", gotKey, gotAuthorization)
	}
	if gotModel != "claude-model-mapped" || gotQuery != "trace_id=anthropic-test" {
		t.Fatalf("upstream model/query = %q/%q", gotModel, gotQuery)
	}
	if result.Status != http.StatusOK || !strings.Contains(writer.Body.String(), `"type":"message"`) {
		t.Fatalf("result/body = %+v/%q", result, writer.Body.String())
	}
}

func TestOpenAICompatibleProviderForwardsAnthropicMessagesStream(t *testing.T) {
	const upstreamSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_stream_test","type":"message","role":"assistant","model":"claude-model-mapped","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello from Anthropic"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`
	var gotPath, gotQuery, gotVersion, gotKey, gotAuthorization, gotModel string
	var gotStream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotVersion = r.Header.Get("Anthropic-Version")
		gotKey, gotAuthorization = r.Header.Get("X-API-Key"), r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}
		gotModel, gotStream = payload.Model, payload.Stream
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("request-id", "req_anthropic_stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, upstreamSSE)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{
		APIKey: "provider-secret", APIKeyHeader: "x-api-key",
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Authorization", "Bearer gateway-caller-secret")
	request.Header.Set("X-API-Key", "gateway-caller-secret")
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request,
		Path: "/v1/messages?trace_id=anthropic-stream",
		Body: []byte(`{"model":"client-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		ModelID: "claude-model-mapped", Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusOK || writer.Code != http.StatusOK {
		t.Fatalf("result/writer status = %d/%d, want 200", result.Status, writer.Code)
	}
	if !strings.HasPrefix(writer.Header().Get("Content-Type"), "text/event-stream") || writer.Body.String() != upstreamSSE {
		t.Fatalf("Anthropic SSE response was not transparently forwarded: content-type=%q body=%q", writer.Header().Get("Content-Type"), writer.Body.String())
	}
	if !strings.Contains(writer.Body.String(), "event: message_start") ||
		!strings.Contains(writer.Body.String(), "hello from Anthropic") ||
		!strings.Contains(writer.Body.String(), "event: message_stop") {
		t.Fatalf("Anthropic streaming lifecycle events are incomplete: %q", writer.Body.String())
	}
	if gotPath != "/v1/messages" || gotQuery != "trace_id=anthropic-stream" {
		t.Fatalf("upstream path/query = %q/%q", gotPath, gotQuery)
	}
	if gotVersion != "2023-06-01" || gotModel != "claude-model-mapped" || !gotStream {
		t.Fatalf("upstream version/model/stream = %q/%q/%v", gotVersion, gotModel, gotStream)
	}
	if gotKey != "provider-secret" || gotAuthorization != "" {
		t.Fatalf("upstream credential isolation failed: x-api-key=%q authorization=%q", gotKey, gotAuthorization)
	}
	if writer.Header().Get("Request-Id") != "req_anthropic_stream" {
		t.Fatalf("Anthropic request ID header was not forwarded: %#v", writer.Header())
	}
}

func TestOpenAICompatibleProviderForwardsAnthropicCountTokensEndpoint(t *testing.T) {
	const upstreamBody = `{"input_tokens":23}`
	var gotPath, gotQuery, gotVersion, gotKey, gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotVersion, gotKey = r.Header.Get("Anthropic-Version"), r.Header.Get("X-API-Key")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}
		gotModel = payload.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{
		APIKey: "provider-secret", APIKeyHeader: "x-api-key",
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("X-API-Key", "gateway-caller-secret")
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request,
		Path: "/v1/messages/count_tokens?trace_id=count-tokens",
		Body: []byte(`{"model":"client-model","messages":[{"role":"user","content":"hi"}]}`),
		ModelID: "claude-model-mapped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusOK || writer.Code != http.StatusOK || writer.Body.String() != upstreamBody {
		t.Fatalf("count_tokens result/body = %+v/%q", result, writer.Body.String())
	}
	if gotPath != "/v1/messages/count_tokens" || gotQuery != "trace_id=count-tokens" {
		t.Fatalf("upstream count_tokens path/query = %q/%q", gotPath, gotQuery)
	}
	if gotVersion != "2023-06-01" || gotModel != "claude-model-mapped" || gotKey != "provider-secret" {
		t.Fatalf("count_tokens header/model/key = %q/%q/%q", gotVersion, gotModel, gotKey)
	}
}

func TestOpenAICompatibleProviderPassesThroughAnthropicJSONErrorAndHeaders(t *testing.T) {
	const upstreamMessage = "context_management: Extra inputs are not permitted"
	const upstreamBody = `{"type":"error","error":{"type":"invalid_request_error","message":"context_management: Extra inputs are not permitted"},"request_id":"req_anthropic_123"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_anthropic_123")
		w.Header().Set("x-should-retry", "false")
		w.Header().Set("anthropic-ratelimit-unified-5h-status", "allowed")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "provider-secret", APIKeyHeader: "x-api-key"})
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	request.Header.Set("Authorization", "Bearer gateway-caller-secret")
	request.Header.Set("X-API-Key", "gateway-caller-secret")
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/messages",
		Body: []byte(`{"model":"claude-model","messages":[]}`), ModelID: "claude-model",
	})
	if err == nil {
		t.Fatal("expected classified upstream error")
	}
	if result.Status != http.StatusBadRequest || writer.Code != http.StatusBadRequest {
		t.Fatalf("upstream/gateway status = %d/%d, want 400/400", result.Status, writer.Code)
	}
	if writer.Body.String() != upstreamBody {
		t.Fatalf("Anthropic error body changed; expected capability-rejection text to pass through unchanged: %q", writer.Body.String())
	}
	if !strings.Contains(writer.Body.String(), upstreamMessage) {
		t.Fatalf("missing upstream capability-rejection text: %q", writer.Body.String())
	}
	if strings.Contains(writer.Body.String(), "provider-secret") || strings.Contains(writer.Body.String(), "gateway-caller-secret") {
		t.Fatalf("credentials leaked through error body: %q", writer.Body.String())
	}
	if writer.Header().Get("Request-Id") != "req_anthropic_123" ||
		writer.Header().Get("X-Should-Retry") != "false" ||
		writer.Header().Get("Anthropic-Ratelimit-Unified-5h-Status") != "allowed" {
		t.Fatalf("Anthropic retry/usage headers were not preserved: %#v", writer.Header())
	}
}

func TestOpenAICompatibleProviderPassesThroughAnthropicStreamErrorBeforeCommit(t *testing.T) {
	const upstreamBody = `{"type":"error","error":{"type":"overloaded_error","message":"server overloaded"},"request_id":"req_busy"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_busy")
		w.WriteHeader(529)
		_, _ = io.WriteString(w, upstreamBody)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "provider-secret", APIKeyHeader: "x-api-key"})
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		Path: "/v1/messages?beta=true", Body: []byte(`{"model":"claude-model","messages":[],"stream":true}`),
		ModelID: "claude-model", Stream: true,
	})
	if err == nil {
		t.Fatal("expected classified upstream error")
	}
	if result.Status != 529 || writer.Code != 529 || writer.Body.String() != upstreamBody {
		t.Fatalf("streaming non-2xx was not passed through unchanged: status=%d/%d body=%q", result.Status, writer.Code, writer.Body.String())
	}
	if writer.Header().Get("Request-Id") != "req_busy" {
		t.Fatalf("request ID missing from response headers: %#v", writer.Header())
	}
}

func TestAnthropicErrorTypeMapsHTTPStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusUnauthorized, "authentication_error"},
		{http.StatusPaymentRequired, "billing_error"},
		{http.StatusForbidden, "permission_error"},
		{http.StatusNotFound, "not_found_error"},
		{http.StatusConflict, "conflict_error"},
		{http.StatusRequestEntityTooLarge, "request_too_large"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusGatewayTimeout, "timeout_error"},
		{529, "overloaded_error"},
		{http.StatusBadGateway, "api_error"},
	}
	for _, tt := range cases {
		if got := anthropicErrorTypeForStatus(tt.status); got != tt.want {
			t.Errorf("status %d: got %q, want %q", tt.status, got, tt.want)
		}
		if got := anthropicErrorType(providerErrorCodeForPath("/v1/messages", tt.status,
			NewHTTPError("test", tt.status, errors.New("mock upstream error")))); got != tt.want {
			t.Errorf("provider error mapping status %d: got %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestOpenAICompatibleProviderRejectsOversizedAnthropicMessagesRequest(t *testing.T) {
	var upstreamCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "provider-secret", APIKeyHeader: "x-api-key"})
	body := make([]byte, AnthropicMessagesMaxRequestBodyBytes+1)
	writer := httptest.NewRecorder()
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		Path: "/v1/messages", Body: body, ModelID: "claude-model",
	})
	if err == nil {
		t.Fatal("expected oversized Anthropic request to be rejected")
	}
	if upstreamCalled.Load() {
		t.Fatal("oversized request reached the upstream")
	}
	if result.Status != http.StatusRequestEntityTooLarge || writer.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d/%d, want 413/413", result.Status, writer.Code)
	}
	var payload struct {
		Type string `json:"type"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(writer.Body.Bytes(), &payload); decodeErr != nil {
		t.Fatalf("Anthropic error body is not JSON: %v", decodeErr)
	}
	if payload.Type != "error" || payload.Error.Type != "request_too_large" {
		t.Fatalf("wrong oversized-request error body: %+v", payload)
	}
}

func TestOpenAICompatibleProviderUsesAnthropicSSEErrorEnvelopeAfterCommit(t *testing.T) {
	const privateError = "upstream Anthropic credential context must not leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"`+privateError+`"}}`)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "provider-secret", APIKeyHeader: "x-api-key"})
	writer := newProviderCommittedWriter()
	_, _ = writer.Write([]byte(": warm start\n\n"))
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/messages", Body: []byte(`{"model":"claude-model","messages":[],"stream":true}`),
		ModelID: "claude-model", Stream: true,
		ClientWriter: func(data []byte) error {
			_, writeErr := writer.Write(data)
			return writeErr
		},
	})
	if err == nil {
		t.Fatal("expected classified upstream error")
	}
	if result.Status != http.StatusServiceUnavailable || writer.recorder.Code != http.StatusOK {
		t.Fatalf("upstream/gateway status = %d/%d, want 503/200", result.Status, writer.recorder.Code)
	}
	body := writer.recorder.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("missing Anthropic SSE error event: %q", body)
	}
	payloadBytes := lastSSEDataPayload(t, body)
	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(payloadBytes, &payload); decodeErr != nil {
		t.Fatalf("Anthropic SSE error payload is not JSON: %v", decodeErr)
	}
	if payload.Type != "error" || payload.Error.Type != "api_error" ||
		payload.Error.Message != "upstream provider request failed" {
		t.Fatalf("incompatible Anthropic SSE error envelope: %+v", payload)
	}
	if strings.Contains(body, privateError) || strings.Contains(body, "provider-secret") {
		t.Fatalf("post-commit fallback SSE event leaked an upstream response body or credential: %q", body)
	}
}

func TestOpenAICompatibleProviderDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		_, _ = w.Write([]byte("{}"))
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer redirector.Close()
	p := testRemoteProvider(t, redirector, OpenAICompatibleConfig{APIKey: "secret-key"})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	result, err := p.Forward(context.Background(), InferenceCall{Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}")})
	if err == nil {
		t.Fatal("redirect should be rejected")
	}
	if redirected.Load() {
		t.Fatal("provider followed redirect to a different host")
	}
	if result.Status != http.StatusFound {
		t.Fatalf("upstream status = %d, want 302", result.Status)
	}
	mapped := ToCoreError(err)
	if mapped == nil || mapped.Status != http.StatusBadGateway {
		t.Fatalf("mapped redirect error = %+v, want 502", mapped)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("redirect body must not be forwarded: %q", recorder.Body.String())
	}
}

func TestOpenAICompatibleProviderStreamsAndReportsCallbacks(t *testing.T) {
	const streamBody = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, streamBody)
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	chunks, headers, firstByte := 0, 0, 0
	res, err := p.Forward(context.Background(), InferenceCall{
		Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}"), Stream: true,
		OnChunk: func([]byte) { chunks++ },
		OnHeaders: func(_ http.Header, status int) {
			if status == http.StatusOK {
				headers++
			}
		},
		ClientWriter: func(data []byte) error {
			_, err := recorder.Write(data)
			return err
		},
		OnFirstByte: func() { firstByte++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusOK || res.ResponseBytes != int64(len(streamBody)) {
		t.Fatalf("result = %+v", res)
	}
	if chunks == 0 || headers != 1 || firstByte != 1 || !strings.Contains(recorder.Body.String(), "hello") {
		t.Fatalf("callbacks/body = %d/%d/%d %q", chunks, headers, firstByte, recorder.Body.String())
	}
}

func TestOpenAICompatibleProviderResponseLimitIsEnforcedBeforeWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "123456")
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{MaxResponseBytes: 5})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	result, err := p.Forward(context.Background(), InferenceCall{Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}")})
	if err == nil {
		t.Fatal("oversized response should fail")
	}
	if result.Status != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("oversize handling = result %+v, body %q", result, recorder.Body.String())
	}
	if mapped := ToCoreError(err); mapped == nil || mapped.Status != http.StatusBadGateway {
		t.Fatalf("mapped error = %+v, want 502", mapped)
	}
}

func TestOpenAICompatibleProviderBlocksPrivateNetworkByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "{}")
	}))
	defer server.Close()
	cfg := OpenAICompatibleConfig{
		ID: "private", BaseURL: strings.Replace(server.URL, "127.0.0.1", "localhost", 1),
		AllowInsecureHTTP: true,
	}
	p, err := NewOpenAICompatibleProvider(cfg)
	if err != nil {
		t.Fatalf("constructor should allow a hostname; dial-time SSRF check should block its private resolution: %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, err = p.Forward(context.Background(), InferenceCall{Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}")})
	if err == nil {
		t.Fatal("private network destination should be blocked by default")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorUpstream {
		t.Fatalf("expected classified upstream error, got %T %v", err, err)
	}
}
 
type fixedRemoteIPResolver struct {
	addresses []netip.Addr
	err       error
}

func (r fixedRemoteIPResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), r.err
}

func TestOpenAICompatibleProviderMapsAndSanitizesDNSFailures(t *testing.T) {
	cases := []struct {
		name       string
		dnsError   *net.DNSError
		wantStatus int
		wantType   string
	}{
		{
			name: "NXDOMAIN",
			dnsError: &net.DNSError{
				Err: "NXDOMAIN_PRIVATE_DIAGNOSTIC", Name: "provider.example", IsNotFound: true,
			},
			wantStatus: http.StatusBadGateway, wantType: "api_error",
		},
		{
			name: "resolver timeout",
			dnsError: &net.DNSError{
				Err: "DNS_TIMEOUT_PRIVATE_DIAGNOSTIC", Name: "provider.example", IsTimeout: true,
			},
			wantStatus: http.StatusGatewayTimeout, wantType: "timeout_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewOpenAICompatibleProviderWithResolver(OpenAICompatibleConfig{
				ID: "dns-test", BaseURL: "https://provider.example/v1",
			}, fixedRemoteIPResolver{err: tc.dnsError})
			if err != nil {
				t.Fatal(err)
			}

			writer := httptest.NewRecorder()
			result, forwardErr := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
				Path: "/v1/messages", Body: []byte(`{"model":"client-model","max_tokens":16,"messages":[]}`),
				ModelID: "server-model",
			})
			if forwardErr == nil {
				t.Fatal("expected DNS transport error")
			}
			if result.Status != tc.wantStatus || writer.Code != tc.wantStatus {
				t.Fatalf("status result/writer = %d/%d, want %d", result.Status, writer.Code, tc.wantStatus)
			}
			var payload struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if decodeErr := json.Unmarshal(writer.Body.Bytes(), &payload); decodeErr != nil {
				t.Fatalf("Anthropic error body is not JSON: %v", decodeErr)
			}
			if payload.Type != "error" || payload.Error.Type != tc.wantType ||
				payload.Error.Message != "upstream provider request failed" {
				t.Fatalf("unexpected sanitized Anthropic error: %+v", payload)
			}
			for _, private := range []string{"provider.example", "NXDOMAIN_PRIVATE_DIAGNOSTIC", "DNS_TIMEOUT_PRIVATE_DIAGNOSTIC"} {
				if strings.Contains(writer.Body.String(), private) || strings.Contains(forwardErr.Error(), private) {
					t.Fatalf("private DNS diagnostic leaked through response/error for %q", private)
				}
			}
		})
	}
}

func TestOpenAICompatibleProviderMapsConnectionRefusedTo502(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	baseURL := probe.URL + "/v1"
	probe.Close()

	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "connection-test", BaseURL: baseURL,
		AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := httptest.NewRecorder()
	result, forwardErr := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		Path: "/v1/messages", Body: []byte(`{"model":"server-model","max_tokens":16,"messages":[]}`),
		ModelID: "server-model",
	})
	if forwardErr == nil {
		t.Fatal("expected connection-refused error")
	}
	if result.Status != http.StatusBadGateway || writer.Code != http.StatusBadGateway {
		t.Fatalf("status result/writer = %d/%d, want 502", result.Status, writer.Code)
	}
	if strings.Contains(writer.Body.String(), "connection refused") || strings.Contains(writer.Body.String(), "127.0.0.1") {
		t.Fatalf("raw dial diagnostic leaked to Anthropic client: %q", writer.Body.String())
	}
	if !strings.Contains(writer.Body.String(), `"type":"api_error"`) {
		t.Fatalf("missing safe Anthropic api_error envelope: %q", writer.Body.String())
	}
}

func TestOpenAICompatibleProviderMapsResponseHeaderTimeoutTo504(t *testing.T) {
	started := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		// 模拟上游接收请求但迟迟不发送响应头；Transport 的 ResponseHeaderTimeout 应中断等待。
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{})
	transport, ok := p.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTP transport type = %T, want *http.Transport", p.client.Transport)
	}
	transport.ResponseHeaderTimeout = 30 * time.Millisecond

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "OpenAI Chat Completions", path: "/v1/chat/completions"},
		{name: "Anthropic Messages", path: "/v1/messages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			result, forwardErr := p.Forward(context.Background(), InferenceCall{
				Writer: writer,
				Request: httptest.NewRequest(http.MethodPost, tc.path, nil),
				Path: tc.path,
				Body: []byte(`{"model":"client-model","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`),
				ModelID: "server-selected-model",
			})
			if forwardErr == nil {
				t.Fatal("expected response-header timeout")
			}
			mapped := ToCoreError(forwardErr)
			if mapped == nil || mapped.Status != http.StatusGatewayTimeout || mapped.Code != "provider_timeout" {
				t.Fatalf("mapped error = %+v (raw %T), want provider_timeout/504", mapped, forwardErr)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("upstream did not receive request before response-header timeout")
			}
			if strings.Contains(forwardErr.Error(), server.URL) ||
				strings.Contains(forwardErr.Error(), "timeout awaiting response headers") {
				t.Fatalf("transport diagnostics leaked through public error: %q", forwardErr.Error())
			}

			if tc.path == "/v1/messages" {
				if result.Status != http.StatusGatewayTimeout || writer.Code != http.StatusGatewayTimeout {
					t.Fatalf("Anthropic result/writer status = %d/%d, want 504", result.Status, writer.Code)
				}
				var payload struct {
					Type  string `json:"type"`
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(writer.Body.Bytes(), &payload); err != nil {
					t.Fatalf("Anthropic timeout body is not JSON: %v", err)
				}
				if payload.Type != "error" || payload.Error.Type != "timeout_error" ||
					payload.Error.Message != "upstream provider request failed" {
					t.Fatalf("unexpected Anthropic timeout envelope: %+v", payload)
				}
				if strings.Contains(writer.Body.String(), server.URL) || strings.Contains(writer.Body.String(), "timeout awaiting response headers") {
					t.Fatalf("transport diagnostics leaked through Anthropic response: %q", writer.Body.String())
				}
			}
		})
	}
}

func TestOpenAICompatibleTransportErrorMapsDeadlineTo504(t *testing.T) {
	p := &OpenAICompatibleProvider{id: "test-remote"}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()

	err := p.transportError(ctx, context.DeadlineExceeded)
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorTimeout {
		t.Fatalf("transport error = %#v, want ProviderError timeout", err)
	}
	mapped := ToCoreError(err)
	if mapped == nil || mapped.Status != http.StatusGatewayTimeout {
		t.Fatalf("mapped timeout = %+v, want HTTP 504", mapped)
	}
}

type providerCommittedWriter struct {
	recorder *httptest.ResponseRecorder
	wrote    bool
}

func newProviderCommittedWriter() *providerCommittedWriter {
	return &providerCommittedWriter{recorder: httptest.NewRecorder()}
}
func (w *providerCommittedWriter) Header() http.Header { return w.recorder.Header() }
func (w *providerCommittedWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.recorder.WriteHeader(status)
}
func (w *providerCommittedWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.recorder.Write(data)
}
func (w *providerCommittedWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	w.recorder.Flush()
}
func (w *providerCommittedWriter) HeaderWritten() bool { return w.wrote }

func TestOpenAICompatibleProviderWritesSSEErrorAfterUpstreamReadFailure(t *testing.T) {
	const firstChunk = `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 声明更长的 Content-Length 并提前结束，模拟上游在 SSE 中途断流。
		w.Header().Set("Content-Length", "256")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, firstChunk)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{})
	writer := newProviderCommittedWriter()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/chat/completions", Body: []byte("{}"), Stream: true,
		ClientWriter: func(data []byte) error {
			_, err := writer.Write(data)
			return err
		},
	})
	if err == nil {
		t.Fatal("expected interrupted stream error")
	}
	if result.Status != http.StatusOK {
		t.Fatalf("upstream status = %d, want original 200", result.Status)
	}
	body := writer.recorder.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, "event: error") {
		t.Fatalf("stream should preserve received content and append an SSE error event: %q", body)
	}
	if strings.Contains(body, "connection refused") || strings.Contains(body, server.URL) {
		t.Fatalf("error event must not expose internal transport detail: %q", body)
	}
}



func TestOpenAICompatibleProviderSanitizesJSONUpstreamStatus(t *testing.T) {
	const privateError = "provider credential details must not leak remote-secret"
	cases := []struct {
		name       string
		status     int
		wantCode   string
		wantKind   ErrorKind
		retryAfter string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: "provider_authentication", wantKind: ErrorAuthentication},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: "provider_rate_limit", wantKind: ErrorRateLimit, retryAfter: "3"},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable, wantCode: "provider_upstream", wantKind: ErrorUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				body, _ := json.Marshal(map[string]interface{}{
					"error": map[string]string{"message": privateError},
					"debug": map[string]string{"credential": "remote-secret"},
				})
				_, _ = w.Write(body)
			}))
			defer server.Close()

			p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "remote-secret"})
			writer := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			result, err := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: request, Path: "/v1/chat/completions",
				Body: []byte("{}"), Stream: false,
			})
			if err == nil {
				t.Fatal("expected classified upstream error")
			}
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) || providerErr.Kind != tc.wantKind {
				t.Fatalf("error = %#v, want ProviderError kind %q", err, tc.wantKind)
			}
			if result.Status != tc.status || writer.Code != tc.status {
				t.Fatalf("upstream/gateway status = %d/%d, want %d", result.Status, writer.Code, tc.status)
			}
			if writer.Header().Get("Content-Type") != "application/json" || writer.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unsafe error headers: %#v", writer.Header())
			}
			if tc.retryAfter != "" && writer.Header().Get("Retry-After") != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", writer.Header().Get("Retry-After"), tc.retryAfter)
			}
			var payload struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &payload); err != nil {
				t.Fatalf("safe error is not JSON: %v; body=%q", err, writer.Body.String())
			}
			if payload.Error.Message != "upstream provider request failed" ||
				payload.Error.Type != "gateway_error" || payload.Error.Code != tc.wantCode {
				t.Fatalf("unexpected safe error payload: %+v", payload.Error)
			}
			if strings.Contains(writer.Body.String(), privateError) || strings.Contains(writer.Body.String(), "remote-secret") {
				t.Fatalf("raw upstream error details leaked: %q", writer.Body.String())
			}
			if result.ResponseBytes != int64(writer.Body.Len()) || string(result.Body) != writer.Body.String() {
				t.Fatalf("result body metadata mismatch: result=%+v body=%q", result, writer.Body.String())
			}
		})
	}
}

func TestOpenAICompatibleProviderSanitizesUpstreamStatusBeforeCommit(t *testing.T) {
	const privateError = "provider credential details must not leak"
	cases := []struct {
		name       string
		status     int
		wantCode   string
		wantKind   ErrorKind
		retryAfter string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantCode: "provider_authentication", wantKind: ErrorAuthentication},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCode: "provider_rate_limit", wantKind: ErrorRateLimit, retryAfter: "3"},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable, wantCode: "provider_upstream", wantKind: ErrorUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				body, _ := json.Marshal(map[string]interface{}{
					"error": map[string]string{"message": privateError},
				})
				_, _ = w.Write(body)
			}))
			defer server.Close()

			p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "remote-secret"})
			writer := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			result, err := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: request, Path: "/v1/chat/completions",
				Body: []byte("{}"), Stream: true,
			})
			if err == nil {
				t.Fatal("expected classified upstream error")
			}
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %T %v, want ProviderError", err, err)
			}
			if providerErr.Kind != tc.wantKind {
				t.Fatalf("ProviderError kind = %q, want %q", providerErr.Kind, tc.wantKind)
			}
			if result.Status != tc.status || writer.Code != tc.status {
				t.Fatalf("upstream/gateway status = %d/%d, want %d", result.Status, writer.Code, tc.status)
			}
			if writer.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", writer.Header().Get("Content-Type"))
			}
			if writer.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", writer.Header().Get("Cache-Control"))
			}
			if tc.retryAfter != "" && writer.Header().Get("Retry-After") != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", writer.Header().Get("Retry-After"), tc.retryAfter)
			}
			var payload struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &payload); err != nil {
				t.Fatalf("safe error is not JSON: %v; body=%q", err, writer.Body.String())
			}
			if payload.Error.Code != tc.wantCode || payload.Error.Type != "gateway_error" {
				t.Fatalf("error payload = %+v, want code=%q type=gateway_error", payload.Error, tc.wantCode)
			}
			if payload.Error.Message != "upstream provider request failed" {
				t.Fatalf("error message = %q", payload.Error.Message)
			}
			if strings.Contains(writer.Body.String(), privateError) || strings.Contains(writer.Body.String(), "remote-secret") {
				t.Fatalf("raw upstream error details leaked: %q", writer.Body.String())
			}
			if int64(writer.Body.Len()) != result.ResponseBytes || string(result.Body) != writer.Body.String() {
				t.Fatalf("result body metadata does not match public error response: result=%+v body=%q", result, writer.Body.String())
			}
		})
	}
}

func TestOpenAICompatibleProviderWritesSafeSSEErrorForUpstreamStatusAfterCommit(t *testing.T) {
	const privateError = "provider credential details must not leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		body, _ := json.Marshal(map[string]interface{}{
			"error": map[string]string{"message": privateError},
		})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "remote-secret"})
	writer := newProviderCommittedWriter()
	_, _ = writer.Write([]byte(": warm start"))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	chunks := 0
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/chat/completions",
		Body: []byte("{}"), Stream: true,
		ClientWriter: func(data []byte) error {
			_, writeErr := writer.Write(data)
			return writeErr
		},
		OnChunk: func([]byte) { chunks++ },
	})
	if err == nil {
		t.Fatal("expected upstream authentication error")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorAuthentication {
		t.Fatalf("error = %T %v, want classified authentication error", err, err)
	}
	if result.Status != http.StatusUnauthorized {
		t.Fatalf("upstream result status = %d, want 401", result.Status)
	}
	if writer.recorder.Code != http.StatusOK {
		t.Fatalf("Gateway status = %d, want previously committed 200", writer.recorder.Code)
	}
	body := writer.recorder.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "provider_authentication") {
		t.Fatalf("response is not a safe SSE error event: %q", body)
	}
	if strings.Contains(body, privateError) || strings.Contains(body, "remote-secret") {
		t.Fatalf("upstream error details leaked into SSE: %q", body)
	}
	if chunks == 0 {
		t.Fatal("safe SSE error chunk should be passed to the observer")
	}
}

func TestOpenAICompatibleProviderWritesResponsesErrorEventAfterCommit(t *testing.T) {
	const privateError = "provider credential details must not leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"`+privateError+`"}}`)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "remote-secret"})
	writer := newProviderCommittedWriter()
	// 模拟 warm_start 已经提交了 200 状态行和一个完整 SSE 注释。
	_, _ = writer.Write([]byte(": warm start\n\n"))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/responses",
		Body: []byte("{}"), Stream: true,
		ClientWriter: func(data []byte) error {
			_, writeErr := writer.Write(data)
			return writeErr
		},
	})
	if err == nil {
		t.Fatal("expected upstream authentication error")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.Kind != ErrorAuthentication {
		t.Fatalf("error = %T %v, want classified authentication error", err, err)
	}
	if result.Status != http.StatusUnauthorized || writer.recorder.Code != http.StatusOK {
		t.Fatalf("upstream/gateway status = %d/%d, want 401/200", result.Status, writer.recorder.Code)
	}
	body := writer.recorder.Body.String()
	payloadBytes := lastSSEDataPayload(t, body)
	var payload struct {
		Type           string  `json:"type"`
		Code           string  `json:"code"`
		Message        string  `json:"message"`
		Param          *string `json:"param"`
		SequenceNumber int64   `json:"sequence_number"`
		Error          any     `json:"error"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("Responses error event data is not JSON: %v", err)
	}
	if !strings.Contains(body, "event: error") ||
		payload.Type != "error" ||
		payload.Code != "provider_authentication" ||
		payload.Message != "upstream provider request failed" ||
		payload.Param != nil ||
		payload.SequenceNumber != 0 ||
		payload.Error != nil {
		t.Fatalf("Responses error event has incompatible shape: body=%q payload=%+v", body, payload)
	}
	if strings.Contains(body, privateError) || strings.Contains(body, "remote-secret") {
		t.Fatalf("upstream error details leaked into Responses SSE: %q", body)
	}
}

func TestOpenAICompatibleProviderWritesResponsesSequenceAfterUpstreamReadFailure(t *testing.T) {
	const firstEvents = "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"hello\"}\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// 提前关闭响应体，模拟已输出部分 Responses 事件后的断流。
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, firstEvents)
	}))
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{})
	writer := newProviderCommittedWriter()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: writer, Request: request, Path: "/v1/responses", Body: []byte("{}"), Stream: true,
		ClientWriter: func(data []byte) error {
			_, writeErr := writer.Write(data)
			return writeErr
		},
	})
	if err == nil {
		t.Fatal("expected interrupted upstream stream error")
	}
	if result.Status != http.StatusOK {
		t.Fatalf("upstream status = %d, want 200", result.Status)
	}
	body := writer.recorder.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, "event: error") {
		t.Fatalf("stream should preserve received Responses events and append error event: %q", body)
	}
	payloadBytes := lastSSEDataPayload(t, body)
	var payload struct {
		Type           string `json:"type"`
		Code           string `json:"code"`
		SequenceNumber int64  `json:"sequence_number"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("terminal Responses error event is not JSON: %v", err)
	}
	if payload.Type != "error" || payload.Code != "provider_upstream" || payload.SequenceNumber != 2 {
		t.Fatalf("terminal Responses error event = %+v, want type=error code=provider_upstream sequence_number=2", payload)
	}
}

func TestResponsesSequenceTrackerHandlesChunkBoundariesAndBoundsMalformedLines(t *testing.T) {
	tracker := &responsesSequenceTracker{}
	chunks := []string{
		"event: response.created\ndata: {\"type\":\"response.created\",",
		"\"sequence_number\":4}\n\nevent: response.output_text.delta\ndata: {\"sequence_number\":5,",
		"\"type\":\"response.output_text.delta\"}\n\n",
	}
	for _, chunk := range chunks {
		tracker.Observe([]byte(chunk))
	}
	if got := tracker.NextSequence(); got != 6 {
		t.Fatalf("NextSequence() = %d, want 6", got)
	}

	oversized := bytes.Repeat([]byte("x"), maxResponsesSSELineBytes+1)
	tracker.Observe(append(oversized, '\n'))
	if got := tracker.NextSequence(); got != 6 {
		t.Fatalf("malformed oversized event changed sequence: got %d, want 6", got)
	}
}

func lastSSEDataPayload(t *testing.T, body string) []byte {
	t.Helper()
	var last []byte
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			last = []byte(value)
		}
	}
	if len(last) == 0 {
		t.Fatalf("SSE response contains no data payload: %q", body)
	}
	return last
}

func TestForbiddenRemoteIPBlocksSpecialUseRanges(t *testing.T) {
	blocked := []string{
		"192.0.2.10", "198.18.0.10", "198.51.100.10", "203.0.113.10",
		"2001:db8::10", "2002:c000:0201::1", "64:ff9b::a00:1",
	}
	for _, raw := range blocked {
		addr := netip.MustParseAddr(raw)
		if !forbiddenRemoteIP(addr) {
			t.Errorf("forbiddenRemoteIP(%s) = false, want true", raw)
		}
	}
}

func TestForbiddenRemoteIPAllowsPublicUnicastExamples(t *testing.T) {
	public := []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"}
	for _, raw := range public {
		addr := netip.MustParseAddr(raw)
		if forbiddenRemoteIP(addr) {
			t.Errorf("forbiddenRemoteIP(%s) = true, want false", raw)
		}
	}
}


func TestOpenAICompatibleProviderPreservesVersionedBasePathPrefix(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "path-prefix", BaseURL: server.URL + "/openai/v1",
		AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleProvider: %v", err)
	}
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: httptest.NewRecorder(),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		Path: "/v1/chat/completions?trace=a%2Fb&debug=1",
		Body: []byte(`{"messages":[]}`),
		ModelID: "model-a",
	})
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if gotPath != "/openai/v1/chat/completions" {
		t.Fatalf("upstream path = %q, want /openai/v1/chat/completions", gotPath)
	}
	if gotQuery != "trace=a%2Fb&debug=1" {
		t.Fatalf("upstream query = %q, want original encoded query", gotQuery)
	}
}

func TestOpenAICompatibleProviderBoundsResponseLimit(t *testing.T) {
	base := OpenAICompatibleConfig{
		ID: "response-limit", BaseURL: "https://api.example.com/v1",
		MaxResponseBytes: maxRemoteResponseLimit,
	}
	p, err := NewOpenAICompatibleProvider(base)
	if err != nil {
		t.Fatalf("maximum supported response limit should be accepted: %v", err)
	}
	if p.maxResponseBytes != maxRemoteResponseLimit {
		t.Fatalf("maxResponseBytes = %d, want %d", p.maxResponseBytes, maxRemoteResponseLimit)
	}

	base.MaxResponseBytes = maxRemoteResponseLimit + 1
	if _, err := NewOpenAICompatibleProvider(base); err == nil {
		t.Fatal("response limit above 512 MiB must be rejected")
	}
}

func TestRestrictedHTTPClientHasBoundedHTTP2Transport(t *testing.T) {
	client := newRestrictedHTTPClient(false)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("custom restricted transport must explicitly enable HTTP/2")
	}
	if transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 {
		t.Fatalf("TLS/header timeouts must be bounded: TLS=%s headers=%s", transport.TLSHandshakeTimeout, transport.ResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout <= 0 || transport.MaxIdleConnsPerHost <= 0 || transport.MaxConnsPerHost <= 0 {
		t.Fatalf("connection pool must have explicit bounds: idle=%s maxIdlePerHost=%d maxPerHost=%d", transport.IdleConnTimeout, transport.MaxIdleConnsPerHost, transport.MaxConnsPerHost)
	}
}


// TestOpenAICompatibleProviderRecordsObservedUpstreamStatus verifies JSON and SSE requests
// report an upstream status only after an HTTP response has actually been received.
func TestOpenAICompatibleProviderRecordsObservedUpstreamStatus(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
			stream, status := stream, status
			mode := "json"
			if stream { mode = "stream" }
			t.Run(mode+"/"+http.StatusText(status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if status != http.StatusOK { w.Header().Set("Retry-After", "7") }
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = io.WriteString(w, `{"id":"recovered"}`)
						return
					}
					_, _ = io.WriteString(w, `{"error":{"message":"private-provider-diagnostic"}}`)
				}))
				defer server.Close()
				p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: "provider-secret"})
				writer := httptest.NewRecorder()
				result, err := p.Forward(context.Background(), InferenceCall{
					Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
					Path: "/v1/chat/completions", Body: []byte(`{"model":"test","stream":true}`), ModelID: "test", Stream: stream,
				})
				if result.Status != status || result.UpstreamStatus != status || writer.Code != status {
					t.Fatalf("status contract result=%d upstream=%d writer=%d, want %d/%d/%d", result.Status, result.UpstreamStatus, writer.Code, status, status, status)
				}
				if status == http.StatusOK {
					if err != nil { t.Fatalf("successful upstream response returned error: %v", err) }
				} else {
					if err == nil { t.Fatal("non-2xx upstream response must return a classified error") }
					if writer.Header().Get("Retry-After") != "7" { t.Fatalf("Retry-After = %q, want 7", writer.Header().Get("Retry-After")) }
					if strings.Contains(writer.Body.String(), "private-provider-diagnostic") || strings.Contains(writer.Body.String(), "provider-secret") {
						t.Fatalf("upstream error body was not redacted: %q", writer.Body.String())
					}
				}
			})
		}
	}
}

func TestOpenAICompatibleProviderConnectionRefusalDoesNotInventUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()
	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "test-remote", BaseURL: serverURL + "/v1", AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	})
	if err != nil { t.Fatalf("NewOpenAICompatibleProvider: %v", err) }
	for _, stream := range []bool{false, true} {
		stream := stream
		mode := "json"
		if stream { mode = "stream" }
		t.Run(mode, func(t *testing.T) {
			writer := httptest.NewRecorder()
			result, forwardErr := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				Path: "/v1/chat/completions", Body: []byte(`{"model":"test","stream":true}`), ModelID: "test", Stream: stream,
			})
			if forwardErr == nil { t.Fatal("expected connection refusal") }
			if result.UpstreamStatus != 0 { t.Fatalf("upstream status = %d, want 0 without an HTTP response", result.UpstreamStatus) }
			mapped := ToCoreError(forwardErr)
			if mapped == nil || mapped.Status != http.StatusBadGateway { t.Fatalf("transport error mapping = %+v, want Gateway 502", mapped) }
		})
	}
}


func TestOpenAICompatibleProviderConcurrencyLimitRejectsExcessRequests(t *testing.T) {
	started := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var upstreamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := upstreamRequests.Add(1)
		if count == 1 {
			started <- struct{}{}
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{MaxConcurrentRequests: 1})

	forward := func(onDispatch func()) (core.Result, error, int) {
		writer := httptest.NewRecorder()
		result, err := p.Forward(context.Background(), InferenceCall{
			Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			OnUpstreamDispatch: onDispatch,
			Path: "/v1/chat/completions", Body: []byte(`{"model":"test"}`), ModelID: "test",
		})
		return result, err, writer.Code
	}
	type outcome struct {
		result core.Result
		err error
		code int
	}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err, code := forward(func() {})
		firstDone <- outcome{result: result, err: err, code: code}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		// 若 handler 已开始等待 releaseFirst，先放行它再触发 Fatal；
		// 否则 defer server.Close 会一直等这个仍被测试卡住的 handler。
		close(releaseFirst)
		t.Fatal("first upstream request did not start")
	}
	var secondDispatch atomic.Int32
	second, secondErr, _ := forward(func() { secondDispatch.Add(1) })
	if secondErr == nil {
		close(releaseFirst)
		t.Fatal("concurrent request should be rejected while the only slot is occupied")
	}
	status, code, _ := core.Unpack(secondErr)
	if status != http.StatusServiceUnavailable || code != "provider_concurrency_limit" {
		close(releaseFirst)
		t.Fatalf("overload error = %d/%s, want 503/provider_concurrency_limit", status, code)
	}
	if second.Status != http.StatusServiceUnavailable || second.UpstreamStatus != 0 {
		close(releaseFirst)
		t.Fatalf("overload result status/upstream = %d/%d, want 503/0", second.Status, second.UpstreamStatus)
	}
	if got := upstreamRequests.Load(); got != 1 {
		close(releaseFirst)
		t.Fatalf("upstream received %d requests while the limit was occupied, want 1", got)
	}
	if got := secondDispatch.Load(); got != 0 {
		close(releaseFirst)
		t.Fatalf("rejected request dispatch callback count = %d, want 0", got)
	}
	close(releaseFirst)
	first := <-firstDone
	if first.err != nil || first.result.Status != http.StatusOK || first.result.UpstreamStatus != http.StatusOK || first.code != http.StatusOK {
		t.Fatalf("first request did not finish successfully: result=%+v err=%v writer=%d", first.result, first.err, first.code)
	}
	var thirdDispatch atomic.Int32
	third, thirdErr, thirdCode := forward(func() { thirdDispatch.Add(1) })
	if thirdErr != nil || third.Status != http.StatusOK || third.UpstreamStatus != http.StatusOK || thirdCode != http.StatusOK {
		t.Fatalf("slot was not released after the first request: result=%+v err=%v writer=%d", third, thirdErr, thirdCode)
	}
	if got := upstreamRequests.Load(); got != 2 {
		t.Fatalf("upstream request count after slot release = %d, want 2", got)
	}
	if got := thirdDispatch.Load(); got != 1 {
		t.Fatalf("admitted request dispatch callback count = %d, want 1", got)
	}
}


func TestOpenAICompatibleProviderSharesConcurrencyLimitAcrossReplacement(t *testing.T) {
	started := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var upstreamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := upstreamRequests.Add(1)
		if count == 1 {
			started <- struct{}{}
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer server.Close()

	oldProvider := testRemoteProvider(t, server, OpenAICompatibleConfig{MaxConcurrentRequests: 2})
	sharedLimiter := oldProvider.ConcurrencyLimiter()
	newProvider := testRemoteProvider(t, server, OpenAICompatibleConfig{
		MaxConcurrentRequests: 1,
		RequestLimiter:        sharedLimiter,
	})
	forward := func(target *OpenAICompatibleProvider) (core.Result, error, int) {
		writer := httptest.NewRecorder()
		result, err := target.Forward(context.Background(), InferenceCall{
			Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			Path: "/v1/chat/completions", Body: []byte(`{"model":"test"}`), ModelID: "test",
		})
		return result, err, writer.Code
	}
	type outcome struct {
		result core.Result
		err    error
		code   int
	}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err, code := forward(oldProvider)
		firstDone <- outcome{result: result, err: err, code: code}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("first request did not reach the upstream")
	}
	// 模拟管理台在旧请求已占用许可时降低 Provider 并发上限。
	if err := sharedLimiter.SetLimit(1); err != nil {
		close(releaseFirst)
		t.Fatal(err)
	}

	second, secondErr, _ := forward(newProvider)
	if secondErr == nil {
		close(releaseFirst)
		t.Fatal("replacement Provider must share the occupied concurrency budget")
	}
	status, code, _ := core.Unpack(secondErr)
	if status != http.StatusServiceUnavailable || code != "provider_concurrency_limit" ||
		second.Status != http.StatusServiceUnavailable || second.UpstreamStatus != 0 {
		close(releaseFirst)
		t.Fatalf("replacement overload result=%+v error=%d/%s, want 503/0 provider concurrency limit", second, status, code)
	}
	if got := upstreamRequests.Load(); got != 1 {
		close(releaseFirst)
		t.Fatalf("overloaded replacement reached upstream: %d requests, want 1", got)
	}
	close(releaseFirst)
	first := <-firstDone
	if first.err != nil || first.result.Status != http.StatusOK || first.result.UpstreamStatus != http.StatusOK || first.code != http.StatusOK {
		t.Fatalf("old in-flight request did not finish normally: result=%+v err=%v writer=%d", first.result, first.err, first.code)
	}
	third, thirdErr, thirdCode := forward(newProvider)
	if thirdErr != nil || third.Status != http.StatusOK || third.UpstreamStatus != http.StatusOK || thirdCode != http.StatusOK {
		t.Fatalf("replacement Provider did not acquire released budget: result=%+v err=%v writer=%d", third, thirdErr, thirdCode)
	}
	if got := upstreamRequests.Load(); got != 2 {
		t.Fatalf("upstream received %d requests after release, want 2", got)
	}
}

func TestOpenAICompatibleProviderWritesSafeSSEErrorWhenLimitReachedAfterCommit(t *testing.T) {
	started := make(chan struct{}, 1)
	chunkSeen := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	var upstreamRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := upstreamRequests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		if count == 1 {
			_, _ = io.WriteString(w, "data: {\"id\":\"first\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			started <- struct{}{}
			<-releaseFirst
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{MaxConcurrentRequests: 1})
	firstRecorder := httptest.NewRecorder()
	type streamOutcome struct {
		result core.Result
		err error
	}
	firstDone := make(chan streamOutcome, 1)
	go func() {
		result, err := p.Forward(context.Background(), InferenceCall{
			Writer: firstRecorder, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			Path: "/v1/chat/completions", Body: []byte(`{"model":"test","stream":true}`), ModelID: "test", Stream: true,
			OnChunk: func([]byte) {
				select {
				case chunkSeen <- struct{}{}:
				default:
				}
			},
		})
		firstDone <- streamOutcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("streaming upstream request did not start")
	}
	select {
	case <-chunkSeen:
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		t.Fatal("first stream chunk was not observed")
	}

	// 模拟 Gateway warm-start 已经向客户端提交 200 的情况。
	committed := newProviderCommittedWriter()
	_, _ = committed.Write([]byte(": warm start\n\n"))
	result, err := p.Forward(context.Background(), InferenceCall{
		Writer: committed, Request: httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		Path: "/v1/responses", Body: []byte(`{"model":"test","stream":true}`), ModelID: "test", Stream: true,
	})
	if err == nil {
		close(releaseFirst)
		t.Fatal("second stream should be rejected at the configured concurrency limit")
	}
	status, code, _ := core.Unpack(err)
	if status != http.StatusServiceUnavailable || code != "provider_concurrency_limit" {
		close(releaseFirst)
		t.Fatalf("overload error = %d/%s, want 503/provider_concurrency_limit", status, code)
	}
	if result.Status != http.StatusServiceUnavailable || result.UpstreamStatus != 0 || committed.recorder.Code != http.StatusOK {
		close(releaseFirst)
		t.Fatalf("committed overload statuses result=%d upstream=%d client=%d", result.Status, result.UpstreamStatus, committed.recorder.Code)
	}
	body := committed.recorder.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "provider_concurrency_limit") || !strings.Contains(body, "\"sequence_number\":0") {
		close(releaseFirst)
		t.Fatalf("missing safe Responses concurrency error event/sequence: %q", body)
	}
	if got := upstreamRequests.Load(); got != 1 {
		close(releaseFirst)
		t.Fatalf("saturated stream reached upstream: requests=%d, want 1", got)
	}
	close(releaseFirst)
	first := <-firstDone
	if first.err != nil || first.result.Status != http.StatusOK || first.result.UpstreamStatus != http.StatusOK || firstRecorder.Code != http.StatusOK {
		t.Fatalf("first stream did not finish normally: result=%+v err=%v", first.result, first.err)
	}
}

func TestNewOpenAICompatibleProviderValidatesConcurrencyLimit(t *testing.T) {
	for _, limit := range []int{-1, MaxOpenAICompatibleConcurrentRequests + 1} {
		_, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
			ID: "invalid-limit", BaseURL: "https://provider.example/v1", MaxConcurrentRequests: limit,
		})
		if err == nil {
			t.Errorf("MaxConcurrentRequests=%d should be rejected", limit)
		}
	}
	for _, tc := range []struct {
		name string
		limit, want int
	}{
		{name: "default", limit: 0, want: defaultOpenAICompatibleMaxConcurrentRequests},
		{name: "custom", limit: 2, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
				ID: "valid-limit", BaseURL: "https://provider.example/v1", MaxConcurrentRequests: tc.limit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if p.requestLimiter.Limit() != tc.want {
				t.Fatalf("effective concurrency = %d, want %d", p.requestLimiter.Limit(), tc.want)
			}
		})
	}
}

func TestNewOpenAICompatibleProviderAppliesRequestTimeout(t *testing.T) {
	for _, seconds := range []int{-1, MaxOpenAICompatibleRequestTimeoutSeconds + 1} {
		_, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
			ID: "timeout-limit", BaseURL: "https://provider.example/v1", RequestTimeoutSeconds: seconds,
		})
		if err == nil {
			t.Errorf("RequestTimeoutSeconds=%d should be rejected", seconds)
		}
	}
	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "timeout-enabled", BaseURL: "https://provider.example/v1", RequestTimeoutSeconds: 120,
	})
	if err != nil { t.Fatal(err) }
	if p.client.Timeout != 120*time.Second { t.Fatalf("client timeout = %s, want 2m", p.client.Timeout) }
	unlimited, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{ID:"timeout-default",BaseURL:"https://provider.example/v1"})
	if err != nil { t.Fatal(err) }
	if unlimited.client.Timeout != 0 { t.Fatalf("default client timeout = %s, want no additional overall timeout", unlimited.client.Timeout) }
}


// TestOpenAICompatibleProviderRecoversAfterStaleIdleConnection 验证服务端关闭空闲 Keep-Alive 连接后，
// Provider 的 HTTP Transport 能从陈旧连接恢复，而不是让后续请求永久卡住或污染响应。
// TestOpenAICompatibleProviderRecoversAfterStaleIdleConnection 验证陈旧空闲连接不会导致后续推理永久失去连接。
func TestOpenAICompatibleProviderRecoversAfterStaleIdleConnection(t *testing.T) {
	var requests atomic.Int32
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	p := testRemoteProvider(t, server, OpenAICompatibleConfig{})
	send := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		result, err := p.Forward(ctx, InferenceCall{
			Writer: recorder,
			Request: request,
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"ping"}]}`),
			ModelID: "test-model",
		})
		if err != nil {
			return err
		}
		if result.Status != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"ok"`) {
			return fmt.Errorf("unexpected response status/body: %d %q", result.Status, recorder.Body.String())
		}
		return nil
	}
	if err := send(); err != nil {
		t.Fatalf("first request failed: %v", err)
	}

	// 模拟 NAT、反向代理或上游清理空闲连接。POST 推理请求不能盲目自动重试，
	// 因为上游可能已经执行；若复用陈旧连接失败，应由后续新请求建立新连接恢复。
	server.CloseClientConnections()
	if err := send(); err != nil {
		mapped := ToCoreError(err)
		if mapped == nil || mapped.Status != http.StatusBadGateway {
			t.Fatalf("stale idle connection error = %v, want a classified 502 without blind retry", err)
		}
	}
	if err := send(); err != nil {
		t.Fatalf("fresh request did not recover after stale keep-alive connection: %v", err)
	}
	if got := requests.Load(); got < 2 || got > 3 {
		t.Fatalf("upstream requests = %d, want 2 (or 3 if the stale request reached the server)", got)
	}
	if got := connections.Load(); got < 2 {
		t.Fatalf("accepted TCP connections = %d, want a replacement connection", got)
	}
}


func TestOpenAICompatibleProviderTestConnectionUsesCredentialAndSanitizesFailures(t *testing.T) {
	const secret = "provider-test-secret"
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer server.Close()
	p := testRemoteProvider(t, server, OpenAICompatibleConfig{APIKey: secret})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	models, err := p.TestConnection(ctx)
	if err != nil { t.Fatal(err) }
	if gotAuth != "Bearer "+secret || strings.Join(models, ",") != "model-a,model-b" {
		t.Fatalf("auth/models = %q/%v", gotAuth, models)
	}

	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"private upstream body"}`)
	}))
	defer failure.Close()
	failedProvider := testRemoteProvider(t, failure, OpenAICompatibleConfig{APIKey: secret})
	_, err = failedProvider.TestConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") { t.Fatalf("expected sanitized HTTP status, got %v", err) }
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private upstream body") || strings.Contains(err.Error(), failure.URL) {
		t.Fatalf("connection test leaked sensitive details: %v", err)
	}
}
