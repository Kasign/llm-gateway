package config

import "testing"

func TestNormalizeLlamaRequestTimeout(t *testing.T) {
	tests := []struct {
		name        string
		configured  *int
		wantSeconds int
		wantErr     bool
	}{
		{name: "omitted retains legacy one hour default", configured: nil, wantSeconds: 3600},
		{name: "explicit zero disables client-wide timeout", configured: intPointer(0), wantSeconds: 0},
		{name: "long local generation can configure one day", configured: intPointer(86400), wantSeconds: 86400},
		{name: "negative timeout rejected", configured: intPointer(-1), wantErr: true},
		{name: "timeout above seven days rejected", configured: intPointer(604801), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Llama.RequestTimeoutSeconds = tt.configured
			err := cfg.normalizeLlamaRequestTimeout()
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeLlamaRequestTimeout() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && cfg.Llama.RequestTimeout != tt.wantSeconds {
				t.Fatalf("RequestTimeout = %d, want %d", cfg.Llama.RequestTimeout, tt.wantSeconds)
			}
		})
	}
}

func intPointer(value int) *int { return &value }
