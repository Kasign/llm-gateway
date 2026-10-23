package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestPublicURL(t *testing.T) {
	ip := httptest.NewRequest(http.MethodGet, "http://10.0.0.8:8080/v1/models?x=1", nil)
	if got := requestPublicURL(ip, nil); got != "http://10.0.0.8:8080/v1/models?x=1" {
		t.Fatalf("ip url: %s", got)
	}

	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8"})
	domain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	domain.RemoteAddr = "10.0.0.12:8088"
	domain.Host = "gateway.internal"
	domain.Header.Set("X-Forwarded-Proto", "https")
	domain.Header.Set("X-Forwarded-Host", "api.example.com, proxy.internal")
	if got := requestPublicURL(domain, trusted); got != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("forwarded url: %s", got)
	}

	plain := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	plain.Host = "10.1.2.3:8080"
	if got := requestPublicURL(plain, nil); got != "http://10.1.2.3:8080/v1/chat/completions" {
		t.Fatalf("host header url: %s", got)
	}

	upper := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	upper.RemoteAddr = "10.0.0.13:8088"
	upper.Host = "api.example.com"
	upper.Header.Set("X-Forwarded-Proto", "HTTPS")
	if got := requestPublicURL(upper, trusted); got != "https://api.example.com/v1/models" {
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
	got := requestPublicURL(r, nil)
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

func TestRedactSensitiveQueryRedactsProviderSpecificKeyNames(t *testing.T) {
	got := redactSensitiveQuery("x-deepseek-key=deepsecret&x-gemini-key=gemsecret&model-key=normal-model&trace_id=abc")
	want := "x-deepseek-key=***&x-gemini-key=***&model-key=normal-model&trace_id=abc"
	if got != want {
		t.Fatalf("redactSensitiveQuery() = %q, want %q", got, want)
	}
}

func TestRequestPublicURLIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/models?token=private", nil)
	r.RemoteAddr = "198.51.100.40:43210"
	r.Host = "gateway.internal"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8"})

	got := requestPublicURL(r, trusted)
	want := "http://gateway.internal/v1/models?token=***"
	if got != want {
		t.Fatalf("requestPublicURL() = %q, want %q", got, want)
	}
}

func TestClientIPStopsAtMalformedForwardedChainEntry(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.12:8088"
	r.Header.Set("X-Forwarded-For", "198.51.100.200, malformed-hop, 10.0.0.23")
	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8"})

	if got := clientIP(r, trusted); got != "10.0.0.12" {
		t.Fatalf("clientIP() = %q, want trusted peer fallback when chain is malformed", got)
	}
}
