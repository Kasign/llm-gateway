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


func TestValidateProviderConfigsChecksQueueLimitsEvenWhenDisabled(t *testing.T) {
	tests := []struct {
		name string
		queueSize int
		waitSeconds int
		wantError bool
	}{
		{name:"negative queue", queueSize:-1, wantError:true},
		{name:"queue above maximum", queueSize:1025, wantError:true},
		{name:"negative wait timeout", queueSize:1, waitSeconds:-1, wantError:true},
		{name:"wait timeout above maximum", queueSize:1, waitSeconds:3601, wantError:true},
		{name:"queue disabled", queueSize:0, waitSeconds:30},
		{name:"queue enabled", queueSize:1024, waitSeconds:3600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Providers.OpenAICompatible = []OpenAICompatibleProviderConfig{{ID:"remote", Enabled:false, MaxQueueSize:tt.queueSize, QueueWaitTimeoutSeconds:tt.waitSeconds}}
			err := cfg.validateProviderConfigs()
			if tt.wantError && err == nil { t.Fatal("invalid queue configuration should be rejected") }
			if !tt.wantError && err != nil { t.Fatalf("valid queue configuration should be accepted: %v", err) }
		})
	}
}
