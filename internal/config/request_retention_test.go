package config

import "testing"

func TestNormalizeLogRetention(t *testing.T) {
	tests := []struct {
		name         string
		days         int
		batch        int
		interval     int
		wantBatch    int
		wantInterval int
		wantErr      bool
	}{
		{name: "disabled by default", days: 0, wantBatch: 500, wantInterval: 3600},
		{name: "enabled with custom safe settings", days: 30, batch: 1000, interval: 1800, wantBatch: 1000, wantInterval: 1800},
		{name: "negative days rejected", days: -1, wantErr: true},
		{name: "excessive batch rejected", days: 1, batch: 5001, wantErr: true},
		{name: "too frequent cleanup rejected", days: 1, interval: 30, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Logging.RetentionDays = tt.days
			cfg.Logging.RetentionBatchSize = tt.batch
			cfg.Logging.RetentionIntervalSeconds = tt.interval
			err := cfg.normalizeLogRetention()
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeLogRetention() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if cfg.Logging.RetentionBatchSize != tt.wantBatch {
					t.Fatalf("retention batch = %d, want %d", cfg.Logging.RetentionBatchSize, tt.wantBatch)
				}
				if cfg.Logging.RetentionIntervalSeconds != tt.wantInterval {
					t.Fatalf("retention interval = %d, want %d", cfg.Logging.RetentionIntervalSeconds, tt.wantInterval)
				}
			}
		})
	}
}
