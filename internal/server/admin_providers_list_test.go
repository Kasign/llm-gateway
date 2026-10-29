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

func TestListManagedProviderViewsUsesUniqueWebIDsAndOmitsLegacyState(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{
		cfg: cfg,
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-chat"},
			"deepseek2": {ID: "deepseek2", Enabled: true, BaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-chat"},
		},
	}

	rows := s.listManagedProviderViews()
	if len(rows) != 2 {
		t.Fatalf("Provider views count = %d, want 2: %#v", len(rows), rows)
	}
	for i, wantID := range []string{"deepseek", "deepseek2"} {
		if rows[i]["id"] != wantID {
			t.Fatalf("row %d id = %#v, want %q", i, rows[i]["id"], wantID)
		}
		for _, staleField := range []string{"source", "editable", "source_conflict", "source_override", "deleted"} {
			if _, exists := rows[i][staleField]; exists {
				t.Fatalf("Provider view still exposes obsolete %q status: %#v", staleField, rows[i])
			}
		}
	}
}

func TestAdminProvidersAPIGetReturnsWebManagedProviderWithoutSecretsOrLegacyFlags(t *testing.T) {
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

	s := &Server{
		cfg: &config.Config{},
		limiter: limiter,
		webLogin: webLoginSnapshot{Username: webAdminIdentity, Revision: 1},
		webProviders: map[string]managedProvider{
			"deepseek": {
				ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com",
				APIKey: "legacy-secret", APIKeyCiphertext: "v1:ciphertext-secret",
				DefaultModel: "deepseek-chat", APIKeyHeader: "authorization",
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
	var payload struct { Data []map[string]interface{} `json:"data"` }
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode provider response: %v; body=%s", err, response.Body.String())
	}
	if len(payload.Data) != 1 {
		t.Fatalf("GET /api/admin/providers returned %d rows, want exactly 1: %#v", len(payload.Data), payload.Data)
	}
	row := payload.Data[0]
	if row["id"] != "deepseek" || row["default_model"] != "deepseek-chat" {
		t.Fatalf("GET /api/admin/providers returned unexpected row: %#v", row)
	}
	body := response.Body.String()
	for _, secret := range []string{"legacy-secret", "ciphertext-secret", "api_key_ciphertext", `"api_key"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("Provider API response leaked %q: %s", secret, body)
		}
	}
	for _, staleField := range []string{"source_override", "deleted", "source_conflict", `"source"`, `"editable"`} {
		if strings.Contains(body, staleField) {
			t.Fatalf("Provider API response still contains obsolete field %q: %s", staleField, body)
		}
	}
	if row["api_key_configured"] != true || row["api_key_masked"] != "••••••••" {
		t.Fatalf("Provider API response should show only masked key status: %#v", row)
	}
}



func TestNormalizeStoredWebProvidersMigratesNormalizedIDs(t *testing.T) {
	rows := []storedManagedProvider{
		{managedProvider: managedProvider{ID: " OpenAI ", Enabled: true, DefaultModel: "gpt-4.1"}},
	}
	got, needsMigration := normalizeStoredWebProviders(rows)
	if !needsMigration {
		t.Fatal("ID normalization should trigger a persistent migration")
	}
	if len(got) != 1 || got["openai"].ID != "openai" {
		t.Fatalf("normalized Provider rows = %#v", got)
	}
}

func TestNormalizeStoredWebProvidersMigratesLegacyLifecycleFlags(t *testing.T) {
	rows := []storedManagedProvider{
		{
			managedProvider: managedProvider{ID: "deepseek", Enabled: false},
			SourceOverride: true, Deleted: true,
		},
		{
			managedProvider: managedProvider{ID: " OpenAI ", Enabled: true, BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4.1"},
			SourceOverride: true,
		},
		{
			managedProvider: managedProvider{ID: "openai", Enabled: true, BaseURL: "https://duplicate.example/v1", DefaultModel: "duplicate-model"},
		},
		{
			managedProvider: managedProvider{ID: "invalid id", Enabled: true},
		},
	}

	got, needsMigration := normalizeStoredWebProviders(rows)
	if !needsMigration {
		t.Fatal("legacy lifecycle flags, duplicate IDs and invalid IDs should trigger migration")
	}
	if _, exists := got["deepseek"]; exists {
		t.Fatal("deleted deployment tombstone should not survive after deployment presets are removed")
	}
	if len(got) != 1 {
		t.Fatalf("normalized Provider count = %d, want 1: %#v", len(got), got)
	}
	if got["openai"].ID != "openai" || got["openai"].DefaultModel != "gpt-4.1" {
		t.Fatalf("first live Provider should win after ID normalization: %#v", got["openai"])
	}
	persisted, err := json.Marshal(providerRecords(got))
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"source_override", "deleted"} {
		if strings.Contains(string(persisted), obsolete) {
			t.Fatalf("new persisted Provider data still contains obsolete field %q: %s", obsolete, persisted)
		}
	}
}
