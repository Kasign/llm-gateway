package llama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLooksLikeOllamaTagsBody(t *testing.T) {
	if !looksLikeOllamaTagsBody([]byte(`{"models":[]}`)) {
		t.Fatal("expected empty models list")
	}
	if looksLikeOllamaTagsBody([]byte(`{"data":[]}`)) {
		t.Fatal("llama.cpp OpenAI list must not pass as ollama")
	}
	if looksLikeOllamaTagsBody([]byte(`<html>ok</html>`)) {
		t.Fatal("html must not pass")
	}
}

func TestOllamaBaseURLIsLibraryHost(t *testing.T) {
	if !UpstreamURLIsOllamaLibrary("https://ollama.com") {
		t.Fatal("ollama.com is library")
	}
	if UpstreamURLIsOllamaLibrary("http://127.0.0.1:11434") {
		t.Fatal("local upstream")
	}
}

func TestReachOllamaNativeRejectsPublicLibraryVersionStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"0.0.0"}`))
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"fake:latest","model":"fake:latest","size":1}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, 2*time.Second, "ollama", "")
	if err := c.Reachable(context.Background()); err == nil {
		t.Fatal("expected reject version 0.0.0 library stub")
	}
}

func TestReachOllamaNativeAcceptsDaemonVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"0.5.4"}`))
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, 2*time.Second, "ollama", "")
	if err := c.Reachable(context.Background()); err != nil {
		t.Fatalf("expected daemon reachable: %v", err)
	}
}

func TestReachOllamaNativeRequiresPSWhenNoVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			http.NotFound(w, r)
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/ps":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, 2*time.Second, "ollama", "")
	if err := c.Reachable(context.Background()); err == nil {
		t.Fatal("expected fail when /api/ps is not ollama daemon")
	}
}
