package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// BenchmarkOpenAICompatibleProviderForwardJSON 测量本地 loopback Mock 上游下的适配器转发开销。
// 它不代表真实供应商网络或模型推理延迟，主要用于同一 runner/Go 版本间比较 ns/op 与分配量。
func BenchmarkOpenAICompatibleProviderForwardJSON(b *testing.B) {
	const responseBody = `{"id":"benchmark-response","choices":[]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()

	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID: "benchmark-provider", BaseURL: server.URL + "/v1",
		AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	})
	if err != nil {
		b.Fatal(err)
	}
	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"benchmark"}]}`)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writer := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		result, err := p.Forward(ctx, InferenceCall{
			Writer: writer, Request: request, Path: "/v1/chat/completions",
			Body: body, ModelID: "benchmark-model",
		})
		if err != nil {
			b.Fatal(err)
		}
		if result.Status != http.StatusOK || writer.Code != http.StatusOK {
			b.Fatalf("status result/writer = %d/%d, want 200", result.Status, writer.Code)
		}
	}
}
