package llama

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type streamProxyRecordingWriter struct {
	header http.Header
	body   bytes.Buffer
	events []string
}

func newStreamProxyRecordingWriter() *streamProxyRecordingWriter {
	return &streamProxyRecordingWriter{header: make(http.Header)}
}

func (w *streamProxyRecordingWriter) Header() http.Header {
	return w.header
}

func (w *streamProxyRecordingWriter) WriteHeader(status int) {
	w.events = append(w.events, "header")
}

func (w *streamProxyRecordingWriter) Write(p []byte) (int, error) {
	w.events = append(w.events, "write")
	return w.body.Write(p)
}

func (w *streamProxyRecordingWriter) Flush() {
	w.events = append(w.events, "flush")
}

func TestStreamProxyFlushesChunkBeforeObserver(t *testing.T) {
	const payload = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(payload))
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()

	client := New(upstream.URL, 5*time.Second, "llamacpp", "")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	writer := newStreamProxyRecordingWriter()

	var observedBytes int
	observerCalls := 0
	err := client.StreamProxy(
		context.Background(),
		writer,
		request,
		"/v1/chat/completions",
		strings.NewReader("{}"),
		func(chunk []byte) {
			observerCalls++
			observedBytes += len(chunk)
			if len(writer.events) == 0 || writer.events[len(writer.events)-1] != "flush" {
				t.Errorf("observer ran before the current chunk was flushed: events=%v", writer.events)
			}
			if writer.body.Len() == 0 {
				t.Error("observer ran before response bytes were written to the client")
			}
			writer.events = append(writer.events, "observer")
		},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("StreamProxy returned error: %v", err)
	}
	if got := writer.body.String(); got != payload {
		t.Fatalf("client body changed during observation: got %q, want %q", got, payload)
	}
	if observerCalls == 0 {
		t.Fatal("observer was not called")
	}
	if observedBytes != len(payload) {
		t.Fatalf("observer saw %d bytes, want %d", observedBytes, len(payload))
	}
}
