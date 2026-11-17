package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/tools"
)

type keyACLTestExecutor struct{}

func (keyACLTestExecutor) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func newKeyACLTestServer(t *testing.T) *Server {
	t.Helper()
	registry := tools.NewRegistry()
	for _, name := range []string{"web.search", "web.fetch"} {
		if err := registry.Register(tools.Definition{Name:name, Description:name, Parameters:json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, keyACLTestExecutor{}); err != nil {
			t.Fatal(err)
		}
	}
	return &Server{
		cfg: &config.Config{Tools: config.ToolsConfig{Enabled:true, AllowedTools:[]string{"web.fetch","web.search","web.unknown"}}},
		toolRegistry: registry,
	}
}

func TestAvailableKeyToolsReturnsRegisteredGlobalAllowlist(t *testing.T) {
	s := newKeyACLTestServer(t)
	got := s.availableKeyTools()
	if len(got) != 2 || got[0] != "web.fetch" || got[1] != "web.search" {
		t.Fatalf("available tools = %v, want sorted registered global allowlist", got)
	}
}

func TestAvailableKeyToolsFailsClosedWhenToolsDisabled(t *testing.T) {
	s := newKeyACLTestServer(t)
	s.cfg.Tools.Enabled = false
	if got := s.availableKeyTools(); len(got) != 0 {
		t.Fatalf("available tools = %v, want empty when globally disabled", got)
	}
}

func TestValidateKeyToolsAcceptsOnlyRegisteredGlobalTools(t *testing.T) {
	s := newKeyACLTestServer(t)
	if err := s.validateKeyTools([]string{"web.search", "web.fetch"}); err != nil {
		t.Fatalf("valid tool grants rejected: %v", err)
	}
	cases := []struct{name string; values []string}{
		{name:"empty", values:[]string{""}},
		{name:"whitespace", values:[]string{" web.search"}},
		{name:"duplicate", values:[]string{"web.search","web.search"}},
		{name:"not globally allowed", values:[]string{"web.admin"}},
		{name:"globally allowed but not registered", values:[]string{"web.unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.validateKeyTools(tc.values); err == nil {
				t.Fatalf("expected invalid grant %v to be rejected", tc.values)
			}
		})
	}
}

func TestValidateKeyToolsRejectsWhenGlobalToolsDisabled(t *testing.T) {
	s := newKeyACLTestServer(t)
	s.cfg.Tools.Enabled = false
	if err := s.validateKeyTools([]string{"web.search"}); err == nil {
		t.Fatal("expected grant rejection while tools are globally disabled")
	}
}

func TestDecodeOptionalAllowedTools(t *testing.T) {
	cases := []struct {
		name string
		raw json.RawMessage
		want []string
		wantNil bool
		wantErr bool
	}{
		{name:"omitted", wantNil:true},
		{name:"null", raw:json.RawMessage(`null`), wantNil:true},
		{name:"whitespace around null", raw:json.RawMessage("  null \n"), wantNil:true},
		{name:"empty array clears", raw:json.RawMessage(`[]`), want:[]string{}},
		{name:"values", raw:json.RawMessage(`["web.search"]`), want:[]string{"web.search"}},
		{name:"object rejected", raw:json.RawMessage(`{}`), wantErr:true},
		{name:"string rejected", raw:json.RawMessage(`"web.search"`), wantErr:true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeOptionalAllowedTools(tc.raw)
			if (err != nil) != tc.wantErr { t.Fatalf("error = %v, wantErr %v", err, tc.wantErr) }
			if tc.wantErr { return }
			if (got == nil) != tc.wantNil { t.Fatalf("got %#v, wantNil %v", got, tc.wantNil) }
			if !tc.wantNil && !reflect.DeepEqual(got, tc.want) { t.Fatalf("got %v, want %v", got, tc.want) }
		})
	}
}
