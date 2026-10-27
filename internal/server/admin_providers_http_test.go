package server

import (
	"bytes"
	"context"
	"errors"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/settings"
)



func TestPrepareProviderConcurrencyLimitAppliesChangesAtSafeTime(t *testing.T) {
	t.Run("lower before registry replacement", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(8)
		if err != nil {
			t.Fatal(err)
		}
		previous, changedBefore, raiseAfter, err := prepareProviderConcurrencyLimit("save", true, true, limiter, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !changedBefore || raiseAfter || previous != 8 || limiter.Limit() != 2 {
			t.Fatalf("lowering policy previous=%d before=%v after=%v limit=%d", previous, changedBefore, raiseAfter, limiter.Limit())
		}
		if err := limiter.SetLimit(previous); err != nil {
			t.Fatal(err)
		}
		if limiter.Limit() != 8 {
			t.Fatalf("rollback limit = %d, want 8", limiter.Limit())
		}
	})

	t.Run("raise only after registry replacement", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil {
			t.Fatal(err)
		}
		previous, changedBefore, raiseAfter, err := prepareProviderConcurrencyLimit("save", true, true, limiter, 8)
		if err != nil {
			t.Fatal(err)
		}
		if changedBefore || !raiseAfter || previous != 2 || limiter.Limit() != 2 {
			t.Fatalf("raising policy previous=%d before=%v after=%v limit=%d", previous, changedBefore, raiseAfter, limiter.Limit())
		}
		if err := limiter.SetLimit(8); err != nil {
			t.Fatal(err)
		}
		if limiter.Limit() != 8 {
			t.Fatalf("limit after simulated Registry success = %d, want 8", limiter.Limit())
		}
	})

	t.Run("do not raise while editing a disabled Provider", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil {
			t.Fatal(err)
		}
		previous, changedBefore, raiseAfter, err := prepareProviderConcurrencyLimit("save", true, false, limiter, 8)
		if err != nil {
			t.Fatal(err)
		}
		if changedBefore || raiseAfter || previous != 2 || limiter.Limit() != 2 {
			t.Fatalf("disabled edit policy previous=%d before=%v after=%v limit=%d", previous, changedBefore, raiseAfter, limiter.Limit())
		}
	})

	t.Run("do not lower the live budget while editing a disabled Provider", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(8)
		if err != nil {
			t.Fatal(err)
		}
		previous, changedBefore, raiseAfter, err := prepareProviderConcurrencyLimit("save", true, false, limiter, 2)
		if err != nil {
			t.Fatal(err)
		}
		if changedBefore || raiseAfter || previous != 8 || limiter.Limit() != 8 {
			t.Fatalf("disabled edit changed runtime budget: previous=%d before=%v after=%v limit=%d", previous, changedBefore, raiseAfter, limiter.Limit())
		}
	})

	t.Run("apply deferred raise when re-enabling", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil {
			t.Fatal(err)
		}
		previous, changedBefore, raiseAfter, err := prepareProviderConcurrencyLimit("enable", true, true, limiter, 8)
		if err != nil {
			t.Fatal(err)
		}
		if changedBefore || !raiseAfter || previous != 2 || limiter.Limit() != 2 {
			t.Fatalf("enable policy previous=%d before=%v after=%v limit=%d", previous, changedBefore, raiseAfter, limiter.Limit())
		}
	})
}

func TestPrepareProviderQueueLimitAppliesChangesAtSafeTime(t *testing.T) {
	t.Run("lower and rollback", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil { t.Fatal(err) }
		if err := limiter.SetQueueLimit(8); err != nil { t.Fatal(err) }
		previous, changed, raise, err := prepareProviderQueueLimit("save", true, true, limiter, 2)
		if err != nil { t.Fatal(err) }
		if previous != 8 || !changed || raise || limiter.QueueLimit() != 2 { t.Fatalf("previous=%d changed=%v raise=%v current=%d", previous, changed, raise, limiter.QueueLimit()) }
		if err := limiter.SetQueueLimit(previous); err != nil { t.Fatal(err) }
		if limiter.QueueLimit() != 8 { t.Fatalf("rollback queue limit=%d, want 8", limiter.QueueLimit()) }
	})
	t.Run("raise after Registry replacement", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil { t.Fatal(err) }
		previous, changed, raise, err := prepareProviderQueueLimit("save", true, true, limiter, 8)
		if err != nil { t.Fatal(err) }
		if previous != 0 || changed || !raise || limiter.QueueLimit() != 0 { t.Fatalf("previous=%d changed=%v raise=%v current=%d", previous, changed, raise, limiter.QueueLimit()) }
		if err := limiter.SetQueueLimit(8); err != nil { t.Fatal(err) }
		if limiter.QueueLimit() != 8 { t.Fatalf("queue limit=%d, want 8", limiter.QueueLimit()) }
	})
	t.Run("disabled edits leave runtime capacity unchanged", func(t *testing.T) {
		for _, tc := range []struct { name string; current, target int }{{"raise", 2, 8}, {"lower", 8, 2}} {
			t.Run(tc.name, func(t *testing.T) {
				limiter, err := provider.NewConcurrencyLimiter(2)
				if err != nil { t.Fatal(err) }
				if err := limiter.SetQueueLimit(tc.current); err != nil { t.Fatal(err) }
				previous, changed, raise, err := prepareProviderQueueLimit("save", true, false, limiter, tc.target)
				if err != nil { t.Fatal(err) }
				if previous != tc.current || changed || raise || limiter.QueueLimit() != tc.current { t.Fatalf("previous=%d changed=%v raise=%v current=%d want=%d", previous, changed, raise, limiter.QueueLimit(), tc.current) }
			})
		}
	})
	t.Run("re-enable defers queue increase", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil { t.Fatal(err) }
		previous, changed, raise, err := prepareProviderQueueLimit("enable", true, true, limiter, 8)
		if err != nil { t.Fatal(err) }
		if previous != 0 || changed || !raise || limiter.QueueLimit() != 0 { t.Fatalf("previous=%d changed=%v raise=%v current=%d", previous, changed, raise, limiter.QueueLimit()) }
	})
	t.Run("reject invalid capacity without mutation", func(t *testing.T) {
		limiter, err := provider.NewConcurrencyLimiter(2)
		if err != nil { t.Fatal(err) }
		if err := limiter.SetQueueLimit(3); err != nil { t.Fatal(err) }
		previous, changed, raise, err := prepareProviderQueueLimit("save", true, true, limiter, provider.MaxOpenAICompatibleQueueSize+1)
		if err == nil { t.Fatal("expected invalid queue capacity error") }
		if previous != 3 || changed || raise || limiter.QueueLimit() != 3 { t.Fatalf("invalid input mutated queue: previous=%d changed=%v raise=%v current=%d", previous, changed, raise, limiter.QueueLimit()) }
	})
}
func TestBuildRemoteProviderReusesManagedConcurrencyLimiter(t *testing.T) {
	limiter, err := provider.NewConcurrencyLimiter(2)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	remote, err := server.buildRemoteProvider(managedProvider{
		ID: "remote", Enabled: true, BaseURL: "https://provider.example/v1", DefaultModel: "model",
		MaxConcurrentRequests: 2, ConcurrencyLimiter: limiter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote.ConcurrencyLimiter() != limiter {
		t.Fatal("Web Provider rebuild must reuse the shared concurrency limiter")
	}
}

func TestProviderAPIKeyEncryptionRoundTripAndRejectsWrongKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x4a}, 32)
	secret := "test-provider-secret"
	ciphertext, err := encryptProviderAPIKey(key, secret)
	if err != nil { t.Fatal(err) }
	if ciphertext == secret || strings.Contains(ciphertext, secret) { t.Fatal("stored representation contains plaintext key") }
	got, err := decryptProviderAPIKey(key, ciphertext)
	if err != nil { t.Fatal(err) }
	if got != secret { t.Fatalf("round trip = %q", got) }
	otherKey := bytes.Repeat([]byte{0x3b}, 32)
	if _, err := decryptProviderAPIKey(otherKey, ciphertext); err == nil { t.Fatal("wrong encryption key must be rejected") }
}

func TestSafeProviderViewNeverExposesPlaintextOrCiphertext(t *testing.T) {
	s := &Server{}
	view := s.safeProviderView(managedProvider{ID:"remote", Enabled:true, APIKey:"plaintext-secret", APIKeyCiphertext:"v1:ciphertext-secret", TokenBudgetPerDay:250000}, "web", true)
	raw, err := json.Marshal(view); if err != nil { t.Fatal(err) }
	text := string(raw)
	for _, forbidden := range []string{"plaintext-secret","ciphertext-secret","api_key_ciphertext",`"api_key"`} { if strings.Contains(text,forbidden) { t.Fatalf("Provider view leaked %q: %s",forbidden,text) } }
	if view["token_budget_per_day"] != int64(250000) { t.Fatalf("safe view token budget = %#v, want 250000", view["token_budget_per_day"]) }
	if view["api_key_configured"] != true { t.Fatal("safe view should disclose whether a key exists") }
}

func TestDeploymentProviderIDIncludesDisabledEntries(t *testing.T) {
	s := &Server{cfg:&config.Config{}}
	s.cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{ID:"reserved-remote",Enabled:false}}
	if !s.deploymentProviderID("RESERVED-REMOTE") { t.Fatal("disabled deployment Provider ID must remain reserved") }
	if s.deploymentProviderID("other") { t.Fatal("unconfigured Provider ID must not be reserved") }
}

 
func TestChangeWebProviderRejectsDeploymentManagedIDIncludingDisabled(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	s.cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{ID:"reserved", Enabled:false}}
	err := s.changeWebProvider(context.Background(), "reserved", &providerMutationRequest{
		ID:"reserved", Enabled:true, BaseURL:"https://example.invalid/v1", DefaultModel:"model",
	}, "save")
	var managedErr *providerManagementError
	if !errors.As(err, &managedErr) || managedErr.status != 409 {
		t.Fatalf("deployment-owned ID should return 409, got %v", err)
	}
}

func TestChangeWebProviderProtectsActiveProviderFromBecomingUnavailable(t *testing.T) {
	actions := []struct {
		name   string
		action string
		input  *providerMutationRequest
	}{
		{name: "disable", action: "disable"},
		{name: "delete", action: "delete"},
		{name: "clear-key", action: "clear-key"},
		{name: "edit-disabled", action: "save", input: &providerMutationRequest{ID: "remote", Enabled: false}},
	}
	for _, tc := range actions {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				cfg: &config.Config{},
				upstream: settings.Upstream{Backend: "remote"},
				webProviders: map[string]managedProvider{
					"remote": {ID: "remote", Enabled: true, BaseURL: "https://example.invalid/v1", DefaultModel: "model"},
				},
			}
			err := s.changeWebProvider(context.Background(), "remote", tc.input, tc.action)
			var managedErr *providerManagementError
			if !errors.As(err, &managedErr) || managedErr.status != 409 || managedErr.code != "active_provider" {
				t.Fatalf("active Provider %s should return 409/active_provider, got %v", tc.action, err)
			}
		})
	}
}
func TestChangeWebProviderRejectsCreateWhenStaleUpstreamPointsAtNewID(t *testing.T) {
	s := &Server{cfg:&config.Config{}, upstream:settings.Upstream{Backend:"orphan"}}
	err := s.changeWebProvider(context.Background(), "orphan", &providerMutationRequest{
		ID:"orphan", Enabled:true, BaseURL:"https://example.invalid/v1", DefaultModel:"model",
	}, "save")
	var managedErr *providerManagementError
	if !errors.As(err, &managedErr) || managedErr.status != 409 {
		t.Fatalf("creating the currently selected but unconfigured ID should return 409, got %v", err)
	}
}

func TestValidateProviderInputKeepsExistingKeyWhenInputIsBlank(t *testing.T) {
	s := &Server{}
	old := managedProvider{
		ID:"remote", Enabled:true, BaseURL:"http://127.0.0.1:9000/v1",
		APIKey:"previous-secret", APIKeyCiphertext:"v1:existing-ciphertext",
		APIKeyHeader:"authorization", DefaultModel:"model",
		AllowInsecureHTTP:true, AllowPrivateNetwork:true,
	}
	next, err := s.validateProviderInput(providerMutationRequest{
		ID:"remote", Enabled:false, BaseURL:old.BaseURL, DefaultModel:"model",
		APIKeyHeader:"authorization", AllowInsecureHTTP:true, AllowPrivateNetwork:true,
	}, &old, "remote")
	if err != nil { t.Fatal(err) }
	if next.APIKey != old.APIKey || next.APIKeyCiphertext != old.APIKeyCiphertext {
		t.Fatalf("blank API Key should retain existing credential; got plaintext=%q ciphertext=%q", next.APIKey, next.APIKeyCiphertext)
	}
}
