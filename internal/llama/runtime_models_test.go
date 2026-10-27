package llama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLlamacppRuntimeFallsBackWhenRouterListIsEmpty(t *testing.T) {
	var routerCalls, openAIListCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			routerCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		case "/v1/models":
			openAIListCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Qwen3.5-9B-Q4_K_M.gguf","object":"model","meta":{"size":4096}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, 2*time.Second, "llamacpp", "")
	snapshot, err := client.RuntimeSnapshot(context.Background(), "")
	if err != nil {
		t.Fatalf("RuntimeSnapshot: %v", err)
	}
	if len(snapshot.Catalog) != 1 || snapshot.Catalog[0].ID != "Qwen3.5-9B-Q4_K_M.gguf" {
		t.Fatalf("catalog = %#v, want the model from /v1/models", snapshot.Catalog)
	}
	if !snapshot.Catalog[0].Loaded || !snapshot.Catalog[0].Running {
		t.Fatalf("single-model endpoint status = %+v, want loaded and running", snapshot.Catalog[0])
	}
	if len(snapshot.Loaded) != 1 || snapshot.ActiveModel != snapshot.Catalog[0].ID {
		t.Fatalf("loaded=%#v active=%q, want the loaded model as active", snapshot.Loaded, snapshot.ActiveModel)
	}

	ids, err := client.ListModelIDs(context.Background())
	if err != nil {
		t.Fatalf("ListModelIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != "Qwen3.5-9B-Q4_K_M.gguf" {
		t.Fatalf("model IDs = %#v, want fallback model ID", ids)
	}
	if routerCalls != 2 || openAIListCalls != 2 {
		t.Fatalf("requests: /models=%d /v1/models=%d, want both endpoints called twice", routerCalls, openAIListCalls)
	}
}

func TestLlamacppRuntimePrefersNonEmptyRouterCatalog(t *testing.T) {
	var openAIListCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"router-model.gguf","status":{"value":"unloaded"},"meta":{"size":8192}}]}`))
		case "/v1/models":
			openAIListCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"currently-loaded.gguf"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, 2*time.Second, "llamacpp", "")
	snapshot, err := client.RuntimeSnapshot(context.Background(), "")
	if err != nil {
		t.Fatalf("RuntimeSnapshot: %v", err)
	}
	if len(snapshot.Catalog) != 1 || snapshot.Catalog[0].ID != "router-model.gguf" {
		t.Fatalf("catalog = %#v, want router catalog", snapshot.Catalog)
	}
	if snapshot.Catalog[0].Loaded {
		t.Fatalf("router model should remain unloaded: %+v", snapshot.Catalog[0])
	}
	if openAIListCalls != 0 {
		t.Fatalf("/v1/models called %d times despite non-empty router catalog", openAIListCalls)
	}
}
