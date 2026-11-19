package llama

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamProxyStopsReadingWhenClientCancels 验证观察器触发客户端取消后，代理不再读取后续数据。
func TestStreamProxyStopsReadingWhenClientCancels(t *testing.T) {
	const firstChunk = "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(firstChunk))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()

	client := New(upstream.URL, 5*time.Second, "llamacpp", "")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	writer := &streamFlushWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	observerCalls := 0
	err := client.StreamProxy(
		ctx,
		writer,
		request,
		"/v1/chat/completions",
		nil,
		func([]byte) {
			observerCalls++
			if writer.flushed == 0 || writer.flushed != writer.body.Len() {
				t.Errorf("observer ran before current chunk flush: flushed=%d body=%d", writer.flushed, writer.body.Len())
			}
			cancel()
		},
		nil,
		nil,
		nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamProxy error = %v, want context.Canceled", err)
	}
	if observerCalls != 1 {
		t.Fatalf("observer calls = %d, want 1", observerCalls)
	}
	if got := writer.body.String(); got != firstChunk {
		t.Fatalf("client body = %q, want first flushed chunk %q", got, firstChunk)
	}
}
