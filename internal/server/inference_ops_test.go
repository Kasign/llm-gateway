package server

import "testing"

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
