package server

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
)

type bufferedTestProvider struct {
	calls int
	last  provider.InferenceCall
	result core.Result
	writerBody []byte
	err   error
}

func (p *bufferedTestProvider) ID() string { return "test" }
func (p *bufferedTestProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{Protocol: "openai-compatible", ChatCompletions: true}
}
func (p *bufferedTestProvider) Forward(_ context.Context, call provider.InferenceCall) (core.Result, error) {
	p.calls++
	p.last = call
	if p.result.Status != 0 {
		call.Writer.Header().Set("Content-Type", "application/json")
		call.Writer.WriteHeader(p.result.Status)
	}
	body := p.result.Body
	if p.writerBody != nil {
		body = p.writerBody
	}
	if len(body) > 0 {
		if _, err := call.Writer.Write(body); err != nil {
			return core.Result{}, err
		}
	}
	return p.result, p.err
}

func TestBufferedResponseWriterCapturesStatusHeadersAndBody(t *testing.T) {
	w := newBufferedResponseWriter(16)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.WriteHeader(http.StatusBadRequest)
	if n, err := w.Write([]byte("payload")); err != nil || n != 7 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if !w.HeaderWritten() || w.status != http.StatusCreated {
		t.Fatalf("unexpected response state: status=%d written=%v", w.status, w.HeaderWritten())
	}
	if got := string(w.bodyBytes()); got != "payload" {
		t.Fatalf("body = %q", got)
	}
}

func TestBufferedResponseWriterRejectsOverflowWithoutPartialWrite(t *testing.T) {
	w := newBufferedResponseWriter(4)
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("5")); n != 0 || !errors.Is(err, errBufferedResponseTooLarge) {
		t.Fatalf("overflow Write() = %d, %v", n, err)
	}
	if got := string(w.bodyBytes()); got != "1234" {
		t.Fatalf("buffer changed after overflow: %q", got)
	}
}

func TestBufferedModelCallerReusesProviderAndCapturesResponse(t *testing.T) {
	body := []byte(`{"choices":[]}`)
	p := &bufferedTestProvider{result: core.Result{Status: http.StatusOK, UpstreamStatus: http.StatusOK, Body: body}}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	req.Header.Set("X-Request-ID", "request-1")
	caller := &bufferedModelCaller{provider: p, request: req, path: "/v1/chat/completions", modelID: "model-stable"}

	response, err := caller.Complete(context.Background(), []byte(`{"messages":[]}`))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if p.calls != 1 || p.last.ModelID != "model-stable" || p.last.Stream {
		t.Fatalf("unexpected Provider call: calls=%d model=%q stream=%v", p.calls, p.last.ModelID, p.last.Stream)
	}
	if p.last.Request.Header.Get("X-Request-ID") != "request-1" || p.last.Request.Method != http.MethodPost {
		t.Fatalf("request metadata was not preserved: %+v", p.last.Request)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != string(body) {
		t.Fatalf("unexpected buffered response: %+v", response)
	}
}

func TestBufferedModelCallerPreservesUpstreamErrorResponse(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limited"}}`)
	upstreamErr := &provider.ProviderError{ProviderID: "test", Kind: provider.ErrorRateLimit, StatusCode: http.StatusTooManyRequests}
	p := &bufferedTestProvider{
		result: core.Result{Status: http.StatusTooManyRequests, UpstreamStatus: http.StatusTooManyRequests, Body: body},
		err: upstreamErr,
	}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	caller := &bufferedModelCaller{provider: p, request: req, path: "/v1/chat/completions", modelID: "model"}

	response, err := caller.Complete(context.Background(), []byte(`{"messages":[]}`))
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("Complete() error = %v, want original Provider error", err)
	}
	if response.StatusCode != http.StatusTooManyRequests || response.UpstreamStatus != http.StatusTooManyRequests ||
		string(response.Body) != string(body) {
		t.Fatalf("upstream error response was not preserved: %+v", response)
	}
}

func TestBufferedModelCallerRejectsMismatchedProviderBody(t *testing.T) {
	p := &bufferedTestProvider{
		result: core.Result{Status: http.StatusOK, Body: []byte("result-body")},
		writerBody: []byte("writer-body"),
	}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	caller := &bufferedModelCaller{provider: p, request: req, path: "/v1/chat/completions", modelID: "model"}
	_, err := caller.Complete(context.Background(), []byte(`{"messages":[]}`))
	if !errors.Is(err, errBufferedResponseMismatch) {
		t.Fatalf("Complete() error = %v, want response mismatch", err)
	}
}

func TestBufferedModelCallerRejectsProviderPanic(t *testing.T) {
	p := &panicBufferedTestProvider{}
	req, _ := http.NewRequest(http.MethodPost, "http://gateway/v1/chat/completions", nil)
	caller := &bufferedModelCaller{provider: p, request: req, path: "/v1/chat/completions", modelID: "model"}
	_, err := caller.Complete(context.Background(), []byte(`{"messages":[]}`))
	if !errors.Is(err, errBufferedProviderPanic) {
		t.Fatalf("Complete() error = %v, want buffered Provider panic", err)
	}
}

type panicBufferedTestProvider struct{}

func (*panicBufferedTestProvider) ID() string { return "panic" }
func (*panicBufferedTestProvider) Capabilities() provider.Capabilities { return provider.Capabilities{} }
func (*panicBufferedTestProvider) Forward(context.Context, provider.InferenceCall) (core.Result, error) {
	panic("private panic detail")
}
