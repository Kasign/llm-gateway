package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticateBenchmarkKeyRequiresDedicatedClientKey(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", nil)
	r.Header.Set("Authorization", "Bearer admin-session-or-other-credential")
	if _, code := s.authenticateBenchmarkKey(r); code != "invalid_api_key" {
		t.Fatalf("authenticateBenchmarkKey() code = %q, want invalid_api_key", code)
	}
}

func TestAuthenticateBenchmarkKeyRejectsMissingKey(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", nil)
	if _, code := s.authenticateBenchmarkKey(r); code != "invalid_api_key" {
		t.Fatalf("authenticateBenchmarkKey() code = %q, want invalid_api_key", code)
	}
}
