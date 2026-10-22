package server

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestClientIPIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8"})
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "198.51.100.40:43210"
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	r.Header.Set("X-Real-IP", "203.0.113.100")
	if got := clientIP(r, trusted); got != "198.51.100.40" {
		t.Fatalf("clientIP() = %q, want actual peer IP", got)
	}
}

func TestClientIPUsesForwardedChainOnlyForTrustedPeer(t *testing.T) {
	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8", "192.0.2.0/24"})
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.12:8088"
	r.Header.Set("X-Forwarded-For", "198.51.100.200, 203.0.113.7, 192.0.2.13")
	r.Header.Set("X-Real-IP", "203.0.113.8")
	if got := clientIP(r, trusted); got != "203.0.113.7" {
		t.Fatalf("clientIP() = %q, want first non-trusted hop from right", got)
	}
}

func TestClientIPFallsBackToRealIPForTrustedPeer(t *testing.T) {
	trusted := parseTrustedProxyRanges([]string{"10.0.0.0/8"})
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.12:8088"
	r.Header.Set("X-Real-IP", "2001:db8::8")
	if got := clientIP(r, trusted); got != "2001:db8::8" {
		t.Fatalf("clientIP() = %q, want trusted X-Real-IP", got)
	}
}

func TestParseTrustedProxyRanges(t *testing.T) {
	got := parseTrustedProxyRanges([]string{"192.0.2.1", "10.0.0.0/8", "bad-entry"})
	if len(got) != 2 {
		t.Fatalf("parsed %d ranges, want 2", len(got))
	}
	if !got[0].Contains(netip.MustParseAddr("192.0.2.1")) || !got[1].Contains(netip.MustParseAddr("10.2.3.4")) {
		t.Fatalf("unexpected trusted proxy prefixes: %#v", got)
	}
}
