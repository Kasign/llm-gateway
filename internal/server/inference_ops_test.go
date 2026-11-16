package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/settings"
)

func TestShouldClearDeletedOllamaActiveModel(t *testing.T) {
	cases := []struct {
		backend  string
		active   string
		deleted  string
		want     bool
	}{
		{backend: "ollama", active: "qwen:latest", deleted: "qwen:latest", want: true},
		{backend: "OLLAMA", active: "Qwen:Latest", deleted: "qwen:latest", want: true},
		{backend: "ollama", active: "qwen:latest", deleted: "llama3:latest", want: false},
		{backend: "llamacpp", active: "qwen:latest", deleted: "qwen:latest", want: false},
		{backend: "ollama", active: "", deleted: "qwen:latest", want: false},
	}
	for _, tc := range cases {
		if got := shouldClearDeletedOllamaActiveModel(tc.backend, tc.active, tc.deleted); got != tc.want {
			t.Errorf("shouldClearDeletedOllamaActiveModel(%q, %q, %q) = %v, want %v",
				tc.backend, tc.active, tc.deleted, got, tc.want)
		}
	}
}

func TestClearDeletedOllamaModelReferences(t *testing.T) {
	u := settings.Upstream{
		Backend: "llamacpp",
		ModelName: "local.gguf",
		ModelsByBackend: map[string]string{
			"ollama": "qwen:latest",
			"llamacpp": "local.gguf",
		},
	}
	got, changed := clearDeletedOllamaModelReferences(u, "Qwen:Latest")
	if !changed {
		t.Fatal("expected stored Ollama model mapping to be cleared")
	}
	if got.ModelName != "local.gguf" {
		t.Fatalf("active non-Ollama model changed: %q", got.ModelName)
	}
	if _, ok := got.ModelsByBackend["ollama"]; ok {
		t.Fatal("deleted Ollama model remained in per-backend model mapping")
	}
	if got.ModelsByBackend["llamacpp"] != "local.gguf" {
		t.Fatalf("unrelated backend mapping changed: %#v", got.ModelsByBackend)
	}
}
