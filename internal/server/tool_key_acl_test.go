package server

import (
	"context"
	"encoding/json"
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
