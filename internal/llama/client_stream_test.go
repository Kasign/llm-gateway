package llama

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type streamFlushWriter struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed int
}

func (w *streamFlushWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *streamFlushWriter) WriteHeader(status int) {
	w.status = status
}

func (w *streamFlushWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *streamFlushWriter) Flush() {
	w.flushed = w.body.Len()
}

func TestStreamProxyFlushesChunkBeforeObserver(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstream.Close()

	client := New(upstream.URL, 2*time.Second, "llamacpp", "")
	request, err := http.NewRequest(http.MethodPost, "http://gateway.test/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := &streamFlushWriter{}
	payload := []byte("{}")
	observed := false
	err = client.StreamProxy(context.Background(), writer, request, "/v1/chat/completions", bytes.NewReader(payload), func([]byte) {
		observed = true
		if writer.flushed != writer.body.Len() || writer.flushed == 0 {
			t.Errorf("observer ran before current chunk flush: flushed=%d body=%d", writer.flushed, writer.body.Len())
		}
	}, nil, nil)
	if err != nil {
		t.Fatalf("StreamProxy: %v", err)
	}
	if !observed {
		t.Fatal("expected onChunk callback")
	}
	if writer.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", writer.status)
	}
}
