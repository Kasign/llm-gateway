package hf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSearchRequestsFullModelFields(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id":"org/model",
			"downloads":3,
			"likes":1,
			"pipeline_tag":"text-generation",
			"lastModified":"2026-10-12T00:00:00Z",
			"createdAt":"2026-01-01T00:00:00Z",
			"tags":["gguf"]
		}]`))
	}))
	defer srv.Close()

	rows, err := (&Client{BaseURL: srv.URL, HTTPClient: srv.Client()}).Search(context.Background(), "qwen", true, true, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "full=true") || !strings.Contains(query, "filter=gguf") || !strings.Contains(query, "sort=lastModified") {
		t.Fatalf("query = %s", query)
	}
	if len(rows) != 1 || rows[0].PipelineTag != "text-generation" {
		t.Fatalf("rows = %+v", rows)
	}
	want := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if !rows[0].PublishedAt.Equal(want) {
		t.Fatalf("published = %s", rows[0].PublishedAt)
	}
}
