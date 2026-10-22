package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
