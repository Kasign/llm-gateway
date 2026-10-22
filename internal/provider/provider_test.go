package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

)

type fakeHTTPClient struct {
	proxyStatus int
	proxyBody   []byte
	streamBody  []byte
	path        string
	method      string
	body        string
}

func (f *fakeHTTPClient) Proxy(_ context.Context, w http.ResponseWriter, r *http.Request, path string, body io.Reader) (int64, []byte, int, error) {
	f.path, f.method = path, r.Method
	requestBody, _ := io.ReadAll(body)
	f.body = string(requestBody)
	w.WriteHeader(f.proxyStatus)
	_, _ = w.Write(f.proxyBody)
	return int64(len(f.proxyBody)), append([]byte(nil), f.proxyBody...), f.proxyStatus, nil
}

func (f *fakeHTTPClient) StreamProxy(_ context.Context, w http.ResponseWriter, r *http.Request, path string, body io.Reader, onChunk func([]byte), onHeader func(http.Header, int), clientWriter func([]byte) error, onFirstByte func()) error {
	f.path, f.method = path, r.Method
	requestBody, _ := io.ReadAll(body)
	f.body = string(requestBody)
	onHeader(http.Header{"Content-Type": []string{"text/event-stream"}}, http.StatusOK)
	w.WriteHeader(http.StatusOK)
	if onFirstByte != nil {
		onFirstByte()
	}
	if clientWriter != nil {
		if err := clientWriter(f.streamBody); err != nil {
			return err
		}
	} else {
		if _, err := w.Write(f.streamBody); err != nil {
			return err
		}
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if onChunk != nil {
		onChunk(f.streamBody)
	}
	return nil
}

func TestRegistryNormalizesAndRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	created := 0
	factory := func() Provider {
		created++
		return NewLocalHTTPProvider("custom", Capabilities{Protocol: "openai-compatible"}, &fakeHTTPClient{})
	}
	if err := r.Register(" Custom ", factory); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("custom", factory); !errors.Is(err, ErrProviderExists) {
		t.Fatalf("duplicate registration error = %v", err)
	}
	p, err := r.Resolve("CUSTOM")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "custom" || created != 1 {
		t.Fatalf("resolved provider=%q factory calls=%d", p.ID(), created)
	}
	if _, err := r.Resolve("missing"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("missing provider error = %v", err)
	}
}

func TestLocalRegistryUsesOneContractForBothBackends(t *testing.T) {
	client := &fakeHTTPClient{proxyStatus: http.StatusCreated, proxyBody: []byte(`{"ok":true}`)}
	registry := NewLocalRegistry(func() LocalHTTPClient { return client })
	for _, id := range []string{"llamacpp", "ollama"} {
		p, err := registry.Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.ID() != id {
			t.Fatalf("provider ID = %q, want %q", p.ID(), id)
		}
		if !p.Capabilities().Streaming || !p.Capabilities().ChatCompletions {
			t.Fatalf("unexpected capabilities for %s: %+v", id, p.Capabilities())
		}
	}
}

func TestLocalHTTPProviderForwardsNonStreamingRequest(t *testing.T) {
	client := &fakeHTTPClient{proxyStatus: http.StatusCreated, proxyBody: []byte(`{"id":"response-1"}`)}
	p := NewLocalHTTPProvider("llamacpp", Capabilities{}, client)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	res, err := p.Forward(context.Background(), InferenceCall{
		Writer: recorder, Request: request, Path: "/v1/chat/completions?include=usage",
		Body: []byte(`{"model":"test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.path != "/v1/chat/completions?include=usage" || client.method != http.MethodPost || client.body != `{"model":"test"}` {
		t.Fatalf("forwarded path/method/body = %q/%q/%q", client.path, client.method, client.body)
	}
	if res.Status != http.StatusCreated || res.ResponseBytes != int64(len(client.proxyBody)) || string(recorder.Body.Bytes()) != string(client.proxyBody) {
		t.Fatalf("unexpected result: %+v body=%q", res, recorder.Body.String())
	}
}

func TestLocalHTTPProviderForwardsStreamCallbacks(t *testing.T) {
	client := &fakeHTTPClient{streamBody: []byte("data: {\"choices\":[]}\n\n")}
	p := NewLocalHTTPProvider("ollama", Capabilities{Streaming: true}, client)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	chunks, headers, firstByte := 0, 0, 0
	res, err := p.Forward(context.Background(), InferenceCall{
		Writer: recorder, Request: request, Path: "/v1/chat/completions", Body: []byte("{}"), Stream: true,
		OnChunk: func([]byte) { chunks++ },
		OnHeaders: func(_ http.Header, status int) {
			if status != http.StatusOK {
				t.Errorf("header status = %d, want 200", status)
			}
			headers++
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
	if res.Status != http.StatusOK || res.ResponseBytes != int64(len(client.streamBody)) {
		t.Fatalf("unexpected stream result: %+v", res)
	}
	if chunks != 1 || headers != 1 || firstByte != 1 {
		t.Fatalf("callback counts chunks/headers/first-byte = %d/%d/%d", chunks, headers, firstByte)
	}
	if !strings.Contains(recorder.Body.String(), "data:") {
		t.Fatalf("stream body = %q", recorder.Body.String())
	}
}

type fakeLocalManager struct {
	readyCalls   int
	loaded       string
	unloaded     string
	refreshCalls int
}

func (m *fakeLocalManager) EnsureReady(context.Context, string, string) error {
	m.readyCalls++
	return nil
}
func (m *fakeLocalManager) Load(_ context.Context, id string) error {
	m.loaded = id
	return nil
}
func (m *fakeLocalManager) Unload(_ context.Context, id string) error {
	m.unloaded = id
	return nil
}
func (m *fakeLocalManager) Refresh(context.Context) error {
	m.refreshCalls++
	return nil
}

func TestLocalRegistryExposesOptionalModelManager(t *testing.T) {
	client := &fakeHTTPClient{}
	manager := &fakeLocalManager{}
	registry := NewLocalRegistry(
		func() LocalHTTPClient { return client },
		func(string) LocalModelManager { return manager },
	)
	p, err := registry.Resolve("llamacpp")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Capabilities().LocalModelManagement {
		t.Fatal("local management capability should be advertised when a manager exists")
	}
	gotManager, ok := registry.ResolveLocalModelManager("LLAMACPP")
	if !ok || gotManager == nil {
		t.Fatal("local manager should resolve case-insensitively")
	}
	if err := gotManager.EnsureReady(context.Background(), "model-key", "upstream-id"); err != nil {
		t.Fatal(err)
	}
	if manager.readyCalls != 1 {
		t.Fatalf("EnsureReady calls = %d, want 1", manager.readyCalls)
	}

	registryWithoutManager := NewLocalRegistry(func() LocalHTTPClient { return client })
	p, err = registryWithoutManager.Resolve("ollama")
	if err != nil {
		t.Fatal(err)
	}
	if p.Capabilities().LocalModelManagement {
		t.Fatal("provider without a manager must not advertise local model management")
	}
	if _, ok := registryWithoutManager.ResolveLocalModelManager("ollama"); ok {
		t.Fatal("provider without local management must not receive local model operations")
	}
}

func TestNewHTTPErrorMapsStableCategories(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantKind  ErrorKind
		retryable bool
	}{
		{name: "bad request", status: http.StatusBadRequest, wantKind: ErrorInvalidRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, wantKind: ErrorAuthentication},
		{name: "forbidden", status: http.StatusForbidden, wantKind: ErrorAuthentication},
		{name: "model missing", status: http.StatusNotFound, wantKind: ErrorModelNotFound},
		{name: "timeout", status: http.StatusRequestTimeout, wantKind: ErrorTimeout, retryable: true},
		{name: "rate limit", status: http.StatusTooManyRequests, wantKind: ErrorRateLimit, retryable: true},
		{name: "bad gateway", status: http.StatusBadGateway, wantKind: ErrorUpstream, retryable: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, wantKind: ErrorUpstream, retryable: true},
		{name: "not implemented", status: http.StatusNotImplemented, wantKind: ErrorUpstream},
		{name: "redirect", status: http.StatusFound, wantKind: ErrorProtocol},
		{name: "unknown", status: 0, wantKind: ErrorUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewHTTPError(" OpenAI-Compatible ", tt.status, errors.New("upstream returned private response body"))
			if err.ProviderID != "openai-compatible" {
				t.Fatalf("ProviderID = %q", err.ProviderID)
			}
			if err.Kind != tt.wantKind || err.Retryable != tt.retryable {
				t.Fatalf("kind/retryable = %q/%v, want %q/%v", err.Kind, err.Retryable, tt.wantKind, tt.retryable)
			}
			if err.StatusCode != tt.status {
				t.Fatalf("StatusCode = %d, want %d", err.StatusCode, tt.status)
			}
			if strings.Contains(err.Error(), "private response body") {
				t.Fatal("public error text must not leak raw upstream response content")
			}
			if !errors.Is(err, err.Err) {
				t.Fatal("ProviderError should preserve the underlying error for internal diagnostics")
			}
		})
	}
}
