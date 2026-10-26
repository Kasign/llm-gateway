package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TLS 握手期间对端关闭 TCP 连接时，上游尚未返回 HTTP 状态；网关应安全映射为 502。
func TestOpenAICompatibleProviderMapsTLSHandshakeInterruptionWithoutUpstreamStatus(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	accepted := make(chan struct{}, 1)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			// 不执行 TLS 握手，模拟上游在握手阶段崩溃或主动断开。
			_ = conn.Close()
		}
	}()
	defer func() {
		_ = listener.Close()
		<-acceptDone
	}()

	p, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		ID:                  "tls-handshake-test",
		BaseURL:             fmt.Sprintf("https://%s/v1", listener.Addr().String()),
		APIKey:              "sk-provider-secret",
		AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatalf("construct test Provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, forwardErr := p.Forward(ctx, InferenceCall{
		Writer:  httptest.NewRecorder(),
		Request: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		Path:    "/v1/chat/completions",
		Body:    []byte("{}"),
		ModelID: "tls-test-model",
	})
	if forwardErr == nil {
		t.Fatal("expected TLS handshake transport error")
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("test listener did not receive a TLS connection")
	}
	mapped := ToCoreError(forwardErr)
	if mapped == nil || mapped.Status != http.StatusBadGateway || mapped.Code != "provider_upstream" {
		t.Fatalf("mapped TLS error = %+v, want safe 502 provider_upstream", mapped)
	}
	if result.UpstreamStatus != 0 {
		t.Fatalf("UpstreamStatus = %d, want 0 because no HTTP response was received", result.UpstreamStatus)
	}
	if strings.Contains(forwardErr.Error(), "127.0.0.1") || strings.Contains(forwardErr.Error(), "sk-provider-secret") {
		t.Fatalf("transport error exposed destination or credential: %q", forwardErr.Error())
	}
}
