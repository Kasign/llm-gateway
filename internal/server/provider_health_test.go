package server

import (
	"context"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/settings"
)

func remoteHealthTestServer(t *testing.T, registerProvider bool) *Server {
	t.Helper()

	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKey: "test-only-key", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
		},
		upstream: settings.Upstream{Backend: "deepseek", BaseURL: "https://api.deepseek.com/v1"},
		providers: provider.NewRegistry(),
	}
	if registerProvider {
		remote, err := provider.NewOpenAICompatibleProvider(provider.OpenAICompatibleConfig{
			ID: "deepseek", BaseURL: "https://api.deepseek.com/v1",
			APIKey: "test-only-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.providers.Register("deepseek", func() provider.Provider { return remote }); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestActiveProviderStatusRemoteDoesNotRequireInactiveLocalClient(t *testing.T) {
	s := remoteHealthTestServer(t, true)

	backend, status, err := s.activeProviderStatus(context.Background())
	if err != nil {
		t.Fatalf("activeProviderStatus() error = %v", err)
	}
	if backend != "deepseek" {
		t.Fatalf("active backend = %q, want deepseek", backend)
	}
	if !strings.Contains(status, "connectivity is checked on inference") {
		t.Fatalf("remote status = %q, should not claim remote reachability", status)
	}
	if s.llama != nil {
		t.Fatal("test requires no local llama client to prove the remote path does not use it")
	}
}

func TestActiveProviderStatusRemoteFailsWhenProviderIsNotRegistered(t *testing.T) {
	s := remoteHealthTestServer(t, false)

	backend, status, err := s.activeProviderStatus(context.Background())
	if err == nil {
		t.Fatal("expected unavailable remote Provider error")
	}
	if backend != "deepseek" || status != "unavailable" {
		t.Fatalf("backend/status = %q/%q, want deepseek/unavailable", backend, status)
	}
}

func TestActiveProviderStatusLocalFailsClosedWhenClientIsMissing(t *testing.T) {
	s := &Server{upstream: settings.Upstream{Backend: "llamacpp"}}

	backend, status, err := s.activeProviderStatus(context.Background())
	if err == nil {
		t.Fatal("expected missing local client error")
	}
	if backend != "llamacpp" || status != "unavailable" {
		t.Fatalf("backend/status = %q/%q, want llamacpp/unavailable", backend, status)
	}
}
