package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/llama"
)

func TestLocalHTTPProviderRecordsObservedUpstreamStatus(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
			stream, status := stream, status
			mode := "json"
			if stream { mode = "stream" }
			t.Run(mode+"/"+http.StatusText(status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"id":"mock-upstream"}`))
				}))
				defer server.Close()
				client := llama.New(server.URL, time.Second, "llamacpp", "")
				p := NewLocalHTTPProvider("llamacpp", Capabilities{Protocol: "openai-compatible"}, client)
				writer := httptest.NewRecorder()
				result, err := p.Forward(context.Background(), InferenceCall{
					Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
					Path: "/v1/chat/completions", Body: []byte(`{"model":"test"}`), ModelID: "test", Stream: stream,
				})
				if err != nil { t.Fatalf("Forward returned error: %v", err) }
				if result.Status != status || result.UpstreamStatus != status || writer.Code != status {
					t.Fatalf("status contract result=%d upstream=%d writer=%d, want %d/%d/%d", result.Status, result.UpstreamStatus, writer.Code, status, status, status)
				}
			})
		}
	}
}

func TestLocalHTTPProviderConnectionRefusalDoesNotInventUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverURL := server.URL
	server.Close()
	client := llama.New(serverURL, time.Second, "llamacpp", "")
	p := NewLocalHTTPProvider("llamacpp", Capabilities{Protocol: "openai-compatible"}, client)
	for _, stream := range []bool{false, true} {
		stream := stream
		mode := "json"
		if stream { mode = "stream" }
		t.Run(mode, func(t *testing.T) {
			writer := httptest.NewRecorder()
			result, err := p.Forward(context.Background(), InferenceCall{
				Writer: writer, Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				Path: "/v1/chat/completions", Body: []byte(`{"model":"test"}`), ModelID: "test", Stream: stream,
			})
			if err == nil { t.Fatal("expected connection refusal") }
			if result.UpstreamStatus != 0 || result.Status != 0 {
				t.Fatalf("transport failure result status/upstream = %d/%d, want 0/0", result.Status, result.UpstreamStatus)
			}
			status, _, _ := core.Unpack(err)
			if status != http.StatusBadGateway { t.Fatalf("transport error maps to HTTP %d, want Gateway 502", status) }
		})
	}
}
