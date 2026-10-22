package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
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
