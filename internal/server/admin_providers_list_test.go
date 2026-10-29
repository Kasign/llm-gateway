package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/ratelimit"
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


func TestAdminProvidersAPIGetDeduplicatesConflictingDeploymentProvider(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDR"))
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; HTTP handler test requires Redis")
	}

	limiter := ratelimit.New(addr, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := limiter.Ping(ctx); err != nil {
		_ = limiter.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	defer limiter.Close()

	token, err := adminuser.NewSessionToken()
	if err != nil {
		t.Fatalf("create admin session token: %v", err)
	}
	if err := limiter.SetSession(ctx, token, webAdminIdentity, 1, time.Minute); err != nil {
		t.Fatalf("save admin test session: %v", err)
	}
	t.Cleanup(func() { _ = limiter.DeleteSession(context.Background(), token) })

	cfg := &config.Config{}
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1",
		APIKeyHeader: "authorization", APIKey: "deployment-secret", DefaultModel: "deepseek-chat",
	}}
	s := &Server{
		cfg:     cfg,
		limiter: limiter,
		webLogin: webLoginSnapshot{
			Username: webAdminIdentity,
			Revision: 1,
		},
		webProviders: map[string]managedProvider{
			"deepseek": {
				ID: "deepseek", Enabled: true, BaseURL: "https://legacy.example/v1",
				APIKey: "legacy-secret", DefaultModel: "legacy-model", SourceConflict: true,
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/providers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	s.adminProvidersAPI(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/providers status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	var payload struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode provider response: %v; body=%s", err, response.Body.String())
	}
	if len(payload.Data) != 1 {
		t.Fatalf("GET /api/admin/providers returned %d rows, want exactly 1: %#v", len(payload.Data), payload.Data)
	}
	row := payload.Data[0]
	if row["id"] != "deepseek" || row["source"] != "deployment" || row["default_model"] != "deepseek-chat" {
		t.Fatalf("GET /api/admin/providers returned unexpected surviving row: %#v", row)
	}
	body := response.Body.String()
	if strings.Contains(body, "deployment-secret") || strings.Contains(body, "legacy-secret") {
		t.Fatalf("Provider API response leaked an API key: %s", body)
	}
	if row["api_key_configured"] != true || row["api_key_masked"] != "••••••••" {
		t.Fatalf("Provider API response should show only masked key status: %#v", row)
	}
}


func TestValidateProviderInputRestoresDeletedDeploymentOverride(t *testing.T) {
	s := &Server{}
	deleted := managedProvider{
		ID: "deepseek",
		Enabled: false,
		BaseURL: "",
		DefaultModel: "",
		SourceOverride: true,
		Deleted: true,
	}
	input := providerMutationRequest{
		ID: "deepseek",
		Enabled: true,
		BaseURL: "https://api.deepseek.com",
		APIKeyHeader: "authorization",
		DefaultModel: "deepseek-chat",
	}

	got, err := s.validateProviderInput(input, &deleted, "deepseek")
	if err != nil {
		t.Fatalf("validateProviderInput restoring deleted deployment override: %v", err)
	}
	if !got.SourceOverride {
		t.Fatal("restoring a deployment-managed Provider must preserve source_override=true")
	}
	if got.Deleted {
		t.Fatal("saving a Provider over a deletion tombstone must clear deleted=true")
	}
	if got.ID != "deepseek" || !got.Enabled || got.DefaultModel != "deepseek-chat" {
		t.Fatalf("restored Provider has unexpected config: %#v", got)
	}
}

func TestValidateProviderInputPreservesExistingDeploymentOverride(t *testing.T) {
	s := &Server{}
	old := managedProvider{
		ID: "openai",
		Enabled: true,
		BaseURL: "https://api.openai.com/v1",
		DefaultModel: "gpt-4.1-mini",
		SourceOverride: true,
	}
	input := providerMutationRequest{
		ID: "openai",
		Enabled: true,
		BaseURL: "https://api.openai.com/v1",
		APIKeyHeader: "authorization",
		DefaultModel: "gpt-4.1-mini",
	}

	got, err := s.validateProviderInput(input, &old, "openai")
	if err != nil {
		t.Fatalf("validateProviderInput editing deployment override: %v", err)
	}
	if !got.SourceOverride || got.Deleted {
		t.Fatalf("editing override must preserve ownership and remain active, got source_override=%v deleted=%v", got.SourceOverride, got.Deleted)
	}
}
