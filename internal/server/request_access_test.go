package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestPublicURL(t *testing.T) {
	ip := httptest.NewRequest(http.MethodGet, "http://10.0.0.8:8080/v1/models?x=1", nil)
	if got := requestPublicURL(ip); got != "http://10.0.0.8:8080/v1/models?x=1" {
		t.Fatalf("ip url: %s", got)
	}

	domain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	domain.Host = "gateway.internal"
	domain.Header.Set("X-Forwarded-Proto", "https")
	domain.Header.Set("X-Forwarded-Host", "api.example.com, proxy.internal")
	if got := requestPublicURL(domain); got != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("forwarded url: %s", got)
	}

	plain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	plain.Host = "10.1.2.3:8080"
	if got := requestPublicURL(plain); got != "http://10.1.2.3:8080/v1/chat/completions" {
		t.Fatalf("host header url: %s", got)
	}

	upper := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	upper.Host = "api.example.com"
	upper.Header.Set("X-Forwarded-Proto", "HTTPS")
	if got := requestPublicURL(upper); got != "https://api.example.com/v1/models" {
		t.Fatalf("uppercase proto: %s", got)
	}
}

func TestSkipCallRecord(t *testing.T) {
	if !skipCallRecord("/health", false) || !skipCallRecord("/static/dist/app.js", false) {
		t.Fatal("health and static should be skipped")
	}
	if !skipCallRecord("/api/requests", true) || !skipCallRecord("/", true) {
		t.Fatal("logged-in console traffic should be skipped")
	}
	if skipCallRecord("/api/models", false) || skipCallRecord("/", false) {
		t.Fatal("unauthenticated console paths should be recorded")
	}
	if skipCallRecord("/v1/chat/completions", true) || skipCallRecord("/models", true) {
		t.Fatal("client paths should be recorded")
	}
}

func TestAccessErrorMessage(t *testing.T) {
	body := []byte(`{"error":{"type":"model_disabled","message":"model disabled"}}`)
	if got := accessErrorMessage(http.StatusForbidden, body); got != "model disabled" {
		t.Fatalf("json message: %s", got)
	}
	if got := accessErrorMessage(http.StatusNotFound, []byte("404 page not found\n")); got != "404 page not found" {
		t.Fatalf("plain message: %s", got)
	}
}

func TestRequestPublicURLRedactsSensitiveQueryValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://api.example.com/v1/models?include=reasoning&token=private-token&client_secret=a%2Bb&trace_id=abc", nil)
	got := requestPublicURL(r)
	want := "https://api.example.com/v1/models?include=reasoning&token=***&client_secret=***&trace_id=abc"
	if got != want {
		t.Fatalf("requestPublicURL() = %q, want %q", got, want)
	}
}

func TestRedactSensitiveQueryPreservesRawEncodingAndOrder(t *testing.T) {
	got := redactSensitiveQuery("metadata%5Bx%5D=a%2Fb&X-Api-Key=secret&include=reasoning&access_token")
	want := "metadata%5Bx%5D=a%2Fb&X-Api-Key=***&include=reasoning&access_token=***"
	if got != want {
		t.Fatalf("redactSensitiveQuery() = %q, want %q", got, want)
	}
}
