package config

import "testing"

func TestValidateProviderConfigsChecksConcurrencyLimitEvenWhenDisabled(t *testing.T) {
	for _, limit := range []int{-1, 65} {
		cfg := &Config{}
		cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{ID: "remote", Enabled: false, MaxConcurrentRequests: limit}}
		if err := cfg.validateProviderConfigs(); err == nil {
			t.Errorf("max_concurrent_requests=%d should be rejected", limit)
		}
	}
	for _, limit := range []int{0, 1, 64} {
		cfg := &Config{}
		cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{ID: "remote", Enabled: false, MaxConcurrentRequests: limit}}
		if err := cfg.validateProviderConfigs(); err != nil {
			t.Errorf("max_concurrent_requests=%d should be accepted: %v", limit, err)
		}
	}
}


func TestValidateProviderConfigsNormalizesAndValidatesModelMappings(t *testing.T) {
	cfg := &Config{}
	cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{
		ID: "remote", Enabled: false, ModelMappings: map[string]string{" public-name ": " upstream-name "},
	}}
	if err := cfg.validateProviderConfigs(); err != nil { t.Fatal(err) }
	if cfg.Providers.OpenAICompatible[0].ModelMappings["public-name"] != "upstream-name" {
		t.Fatalf("mapping not normalized: %#v", cfg.Providers.OpenAICompatible[0].ModelMappings)
	}
	cfg.Providers.OpenAICompatible[0].ModelMappings = map[string]string{"": "upstream"}
	if err := cfg.validateProviderConfigs(); err == nil { t.Fatal("blank public model ID must be rejected") }
}
