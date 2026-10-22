package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/whl/llm-gateway/internal/core"
)

func TestSanitizeKeepsTools(t *testing.T) {
	cases := []struct {
		path string
		body string
	}{
		{
			path: "/v1/responses",
			body: `{
				"stream": false,
				"instructions": "sys",
				"input": [{"role":"user","content":"hi"}],
				"tools": [
					{"type":"function","name":"exec_command"},
					{"type":"namespace","name":"mcp__search__","tools":[{"type":"function","name":"search"}]},
					{"type":"web_search"}
				]
			}`,
		},
		{
			path: "/v1/chat/completions",
			body: `{
				"stream": false,
				"messages": [{"role":"user","content":"hi"}],
				"tools": [
					{"type":"function","function":{"name":"exec_command"}},
					{"type":"namespace","name":"mcp__search__"},
					{"type":"web_search"}
				]
			}`,
		},
	}
	for _, tc := range cases {
		out := sanitizeForLlama(tc.path, []byte(tc.body))
		var obj map[string]interface{}
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatal(tc.path, err)
		}
		tools, ok := obj["tools"].([]interface{})
		if !ok || len(tools) != 3 {
			t.Fatalf("%s tools=%#v", tc.path, obj["tools"])
		}
	}
}

func TestMutateResponsesBridgeKeepsPathAndBody(t *testing.T) {
	body := []byte(`{"tools":[{"type":"namespace","name":"mcp"}]}`)
	st := core.State{
		UpstreamPath: "/v1/responses",
	}
	st.Path = "/v1/responses"
	st.Body = append([]byte(nil), body...)
	out, err := (&Server{}).mutateResponsesBridge(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if out.UpstreamPath != "/v1/responses" || out.Path != "/v1/responses" {
		t.Fatalf("path=%s upstream=%s", out.Path, out.UpstreamPath)
	}
	if string(out.Body) != string(body) {
		t.Fatalf("body changed: %s", out.Body)
	}
}
