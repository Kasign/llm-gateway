package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchDetailedReportsPartialSourceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{{"id":"org/example","downloads":12}})
		case "/search":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body>challenge page without model links</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService(Config{HFBaseURL: server.URL, OllamaLibraryURL: server.URL})
	rows, sources, err := svc.SearchDetailed(context.Background(), Query{Text:"example", Source:"all", Limit:10})
	if err != nil {
		t.Fatalf("partial search returned error: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "org/example" {
		t.Fatalf("successful HF results were not preserved: %+v", rows)
	}
	if sources[string(SourceHuggingFace)].Status != "success" {
		t.Fatalf("HF status = %+v", sources[string(SourceHuggingFace)])
	}
	if got := sources[string(SourceOllama)]; got.Status != "failed" || got.Error == "" {
		t.Fatalf("Ollama failure was hidden: %+v", got)
	}
}
