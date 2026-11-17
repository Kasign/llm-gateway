package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/tools"
	"github.com/whl/llm-gateway/internal/tools/modules/searxng"
	toolruntime "github.com/whl/llm-gateway/internal/tools/runtime"
)

type requestToolAllowlistContextKey struct{}

type requestToolAuthorizer struct{}

func (requestToolAuthorizer) Authorize(ctx context.Context, toolName string) (bool, error) {
	if ctx == nil {
		return false, nil
	}
	allowed, ok := ctx.Value(requestToolAllowlistContextKey{}).(map[string]struct{})
	if !ok {
		return false, nil
	}
	_, ok = allowed[toolName]
	return ok, nil
}

func withRequestToolAllowlist(ctx context.Context, names []string) context.Context {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return context.WithValue(ctx, requestToolAllowlistContextKey{}, allowed)
}

func (s *Server) initToolSystem() error {
	registry := tools.NewRegistry()
	if s == nil {
		return fmt.Errorf("server is nil")
	}
	if s.cfg == nil {
		s.toolRegistry = registry
		runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
		if err != nil {
			return err
		}
		s.toolRuntime = runtime
		return nil
	}
	if err := s.cfg.Tools.Normalize(); err != nil {
		return err
	}
	if !s.cfg.Tools.Enabled {
		s.toolRegistry = registry
		runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
		if err != nil {
			return err
		}
		s.toolRuntime = runtime
		return nil
	}

	for _, name := range s.cfg.Tools.AllowedTools {
		switch name {
		case "web.search":
			executor, err := searxng.New(searxng.Config{
				BaseURL: s.cfg.Tools.SearXNG.BaseURL,
				AllowInsecureHTTP: s.cfg.Tools.SearXNG.AllowInsecureHTTP,
				AllowPrivateNetwork: s.cfg.Tools.SearXNG.AllowPrivateNetwork,
				RequestTimeout: time.Duration(s.cfg.Tools.SearXNG.RequestTimeoutSeconds) * time.Second,
				MaxResults: s.cfg.Tools.SearXNG.MaxResults,
				MaxResponseBytes: s.cfg.Tools.SearXNG.MaxResponseBytes,
				SafeSearch: s.cfg.Tools.SearXNG.SafeSearch,
				DefaultLanguage: s.cfg.Tools.SearXNG.DefaultLanguage,
				Categories: append([]string(nil), s.cfg.Tools.SearXNG.Categories...),
			})
			if err != nil {
				return fmt.Errorf("initialize web.search: %w", err)
			}
			if err := registry.Register(searxng.Definition(), executor); err != nil {
				return fmt.Errorf("register web.search: %w", err)
			}
		default:
			return fmt.Errorf("configured tool %q is not registered in this build", name)
		}
	}
	for _, name := range s.cfg.Tools.AllowedTools {
		if _, _, err := registry.Lookup(name); err != nil {
			return fmt.Errorf("configured tool %q is unavailable: %w", name, err)
		}
	}
	runtime, err := toolruntime.NewRuntime(registry, requestToolAuthorizer{}, toolruntime.Config{})
	if err != nil {
		return fmt.Errorf("initialize tool runtime: %w", err)
	}
	s.toolRegistry = registry
	s.toolRuntime = runtime
	return nil
}

func (s *Server) configuredToolNames(st core.State) []string {
	if s == nil || s.cfg == nil || !s.cfg.Tools.Enabled || s.toolInitErr != nil ||
		s.toolRegistry == nil || s.toolRuntime == nil {
		return nil
	}
	providerID := strings.ToLower(strings.TrimSpace(st.Model.ProviderID))
	modelKey := strings.TrimSpace(st.Model.ModelKey)
	if providerID == "" || modelKey == "" {
		return nil
	}
	modelAllowed := false
	for _, item := range s.cfg.Tools.AllowedModels {
		if strings.EqualFold(item.ProviderID, providerID) && item.ModelKey == modelKey {
			modelAllowed = true
			break
		}
	}
	if !modelAllowed {
		return nil
	}
	names := make([]string, 0, len(s.cfg.Tools.AllowedTools))
	for _, name := range s.cfg.Tools.AllowedTools {
		if _, _, err := s.toolRegistry.Lookup(name); err == nil {
			names = append(names, name)
		}
	}
	return names
}

func requestHasToolIntent(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return false
	}
	_, hasTools := object["tools"]
	_, hasChoice := object["tool_choice"]
	_, hasLegacyFunctions := object["functions"]
	_, hasLegacyFunctionCall := object["function_call"]
	return hasTools || hasChoice || hasLegacyFunctions || hasLegacyFunctionCall
}

func isChatCompletionsPath(path string) bool {
	path = strings.TrimRight(strings.TrimSpace(strings.SplitN(path, "?", 2)[0]), "/")
	return path == "/v1/chat/completions"
}

func requestUsesSingleChoice(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return false
	}
	raw, exists := object["n"]
	if !exists {
		return true
	}
	var count int
	return json.Unmarshal(raw, &count) == nil && count == 1
}
