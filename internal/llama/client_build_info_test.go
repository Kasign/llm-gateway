package llama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildVersionReadsLlamaCPPProps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" { t.Fatalf("path = %q, want /props", r.URL.Path) }
		_, _ = w.Write([]byte(`{"build_info":"b4123-abcdef"}`))
	}))
	defer server.Close()
	client := New(server.URL, time.Second, "llamacpp", "")
	version, source, err := client.BuildVersion(context.Background())
	if err != nil { t.Fatal(err) }
	if version != "b4123-abcdef" || source != "llama_cpp_props.build_info" { t.Fatalf("BuildVersion() = %q, %q", version, source) }
}

func TestBuildVersionReadsOllamaAPIVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" { t.Fatalf("path = %q, want /api/version", r.URL.Path) }
		_, _ = w.Write([]byte(`{"version":"0.12.3"}`))
	}))
	defer server.Close()
	client := New(server.URL, time.Second, "ollama", "")
	version, source, err := client.BuildVersion(context.Background())
	if err != nil { t.Fatal(err) }
	if version != "0.12.3" || source != "ollama_api.version" { t.Fatalf("BuildVersion() = %q, %q", version, source) }
}

func TestBuildVersionKeepsMissingVersionUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	client := New(server.URL, time.Second, "llamacpp", "")
	if version, _, err := client.BuildVersion(context.Background()); err == nil || version != "" {
		t.Fatalf("missing build_info must remain unknown, got version=%q err=%v", version, err)
	}
}
