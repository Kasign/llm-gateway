package server

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/settings"
)

func TestRemoteProviderMutatorsPreserveOriginalRequestBody(t *testing.T) {
	body := []byte(`{"stream":true,"instructions":"keep instructions","input":[{"role":"developer","content":"keep developer"},{"role":"user","content":"hello"}],"temperature":0.13}`)
	s := &Server{upstream: settings.Upstream{Backend: "deepseek"}}
	st := core.State{Path: "/v1/responses", Body: append([]byte(nil), body...)}

	got, err := s.mutateSanitize(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.mutateInferenceParams(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("remote request body was mutated\n got: %s\nwant: %s", got.Body, body)
	}
}

func TestLocalProviderSanitizerStillMergesSystemAndStreamUsage(t *testing.T) {
	body := []byte(`{"stream":true,"input":[{"role":"developer","content":"system rule"},{"role":"user","content":"hello"}]}`)
	s := &Server{upstream: settings.Upstream{Backend: "llamacpp"}}
	got, err := s.mutateSanitize(context.Background(), core.State{Path: "/v1/responses", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(got.Body, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["instructions"] != "system rule" {
		t.Fatalf("instructions = %#v", obj["instructions"])
	}
	input, _ := obj["input"].([]interface{})
	if len(input) != 1 {
		t.Fatalf("input length = %d, want only the user message", len(input))
	}
	options, _ := obj["stream_options"].(map[string]interface{})
	if options["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage=true", options)
	}
}
