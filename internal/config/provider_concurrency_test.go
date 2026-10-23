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
