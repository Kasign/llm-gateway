package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeBenchmarkRequestBodyAcceptsSingleJSONValue(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"prompt":"hello"}`))
	w := httptest.NewRecorder()
	var got struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeBenchmarkRequestBody(w, r, &got, 1024); err != nil {
		t.Fatalf("decodeBenchmarkRequestBody() error = %v", err)
	}
	if got.Prompt != "hello" {
		t.Fatalf("decoded prompt = %q, want hello", got.Prompt)
	}
}

func TestDecodeBenchmarkRequestBodyRejectsTrailingJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"prompt":"hello"} {}`))
	w := httptest.NewRecorder()
	var got map[string]any
	if err := decodeBenchmarkRequestBody(w, r, &got, 1024); err == nil {
		t.Fatal("decodeBenchmarkRequestBody() must reject trailing JSON")
	}
}

func TestDecodeBenchmarkRequestBodyEnforcesLimit(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/benchmark", strings.NewReader(`{"x":1}`))
	w := httptest.NewRecorder()
	var got map[string]any
	if err := decodeBenchmarkRequestBody(w, r, &got, 4); err == nil {
		t.Fatal("decodeBenchmarkRequestBody() must reject an oversized body")
	}
}
