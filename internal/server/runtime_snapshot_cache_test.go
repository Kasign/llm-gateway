package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/llama"
)

func TestUpstreamRuntimeSnapshotCacheInvalidatesWhenUpstreamStops(t *testing.T) {
	var tagsHits atomic.Int32
	var upstreamStopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			tagsHits.Add(1)
			if upstreamStopped.Load() {
				http.Error(w, "upstream stopped", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"m:latest","model":"m:latest","size":1}]}`))
		case "/api/ps":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	s := &Server{}
	client := llama.New(srv.URL, 2*time.Second, "ollama", "")
	ctx := context.Background()

	snap1, err := s.upstreamRuntimeSnapshot(ctx, "ollama", client, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap1 == nil || len(snap1.Catalog) != 1 {
		t.Fatalf("expected 1 catalog row, got %#v err=%v", snap1, err)
	}

	// 保留测试服务以便统计刷新请求；通过返回 503 模拟上游已不可用。
	upstreamStopped.Store(true)
	s.invalidateUpstreamRuntimeSnapshotCache()

	snap2, err2 := s.upstreamRuntimeSnapshot(ctx, "ollama", client, "")
	if err2 == nil && snap2 != nil && len(snap2.Catalog) == 1 {
		t.Fatal("expected cache miss / error after upstream stopped, still got catalog")
	}
	if tagsHits.Load() < 2 {
		t.Fatalf("expected at least 2 tag fetches (initial + after stop), got %d", tagsHits.Load())
	}
}
