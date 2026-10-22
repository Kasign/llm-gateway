package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/store"
)

func TestBuildRequestParamsFromForwardedBody(t *testing.T) {
	l := &store.RequestLog{ModelRequested: "gpt", ModelActual: "qwen", Stream: true}
	body := []byte(`{
		"stream":true,
		"stream_options":{"include_usage":true},
		"enable_thinking":false,
		"max_tokens":2048,
		"temperature":0.2
	}`)
	p := buildRequestParams(l, body)
	if p["stream_label"] != "是" {
		t.Fatalf("stream=%v", p["stream_label"])
	}
	if p["thinking"] != "关闭" {
		t.Fatalf("thinking=%v", p["thinking"])
	}
	if p["include_usage"] != true {
		t.Fatalf("include_usage=%v", p["include_usage"])
	}
	sp, _ := p["service_params"].(map[string]interface{})
	if sp["max_tokens"] == nil || sp["temperature"] == nil {
		t.Fatalf("service params=%v", sp)
	}
}
