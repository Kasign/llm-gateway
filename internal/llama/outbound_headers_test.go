package llama

import (
	"context"
	"net/http"
	"testing"
)

func TestEmitOutboundHeadersCopiesActualRequest(t *testing.T) {
	var got http.Header
	ctx := WithOutboundHeaderProbe(context.Background(), func(h http.Header) {
		got = h.Clone()
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://up.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Codex-Extra", "yes")
	req.ContentLength = 12
	emitOutboundHeaders(ctx, req)
	if got.Get("Accept") != "text/event-stream" || got.Get("X-Codex-Extra") != "yes" {
		t.Fatalf("missing copied headers: %#v", got)
	}
	if got.Get("Host") != "up.example" {
		t.Fatalf("host=%q", got.Get("Host"))
	}
	if got.Get("Content-Length") != "12" {
		t.Fatalf("content-length=%q", got.Get("Content-Length"))
	}
}

func TestEmitOutboundHeadersNoProbe(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://up.example/", nil)
	emitOutboundHeaders(context.Background(), req) // 不应 panic
}
