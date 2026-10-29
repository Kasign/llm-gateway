package server

import (
	"testing"

	"github.com/whl/llm-gateway/internal/config"
)

func TestListManagedProviderViewsDoesNotDuplicateDeploymentAndWebRecords(t *testing.T) {
	tests := []struct {
		name       string
		webRow     managedProvider
		wantCount  int
		wantSource string
		wantModel  string
	}{
		{
			name: "legacy conflicting Web row only shows deployment Provider",
			webRow: managedProvider{
				ID: "deepseek", Enabled: true, BaseURL: "https://legacy.example/v1",
				DefaultModel: "legacy-model", SourceConflict: true,
			},
			wantCount: 1, wantSource: "deployment", wantModel: "deepseek-chat",
		},
		{
			name: "explicit override shows only Web Provider",
			webRow: managedProvider{
				ID: "deepseek", Enabled: true, BaseURL: "https://override.example/v1",
				DefaultModel: "override-model", SourceOverride: true,
			},
			wantCount: 1, wantSource: "web-override", wantModel: "override-model",
		},
		{
			name: "deleted override hides both deployment and Web row",
			webRow: managedProvider{
				ID: "deepseek", Enabled: false, SourceOverride: true, Deleted: true,
			},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
				ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1",
				APIKeyHeader: "authorization", APIKey: "deployment-secret", DefaultModel: "deepseek-chat",
			}}
			s := &Server{
				cfg: cfg,
				webProviders: map[string]managedProvider{
					"deepseek": tc.webRow,
				},
			}

			rows := s.listManagedProviderViews()
			if len(rows) != tc.wantCount {
				t.Fatalf("Provider views count = %d, want %d: %#v", len(rows), tc.wantCount, rows)
			}
			if tc.wantCount == 0 {
				return
			}
			if got := rows[0]["source"]; got != tc.wantSource {
				t.Fatalf("Provider source = %#v, want %q", got, tc.wantSource)
			}
			if got := rows[0]["default_model"]; got != tc.wantModel {
				t.Fatalf("Provider default_model = %#v, want %q", got, tc.wantModel)
			}
		})
	}
}
