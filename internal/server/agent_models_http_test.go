package server

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/ratelimit"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
)

var agentModelsTestDriverSequence atomic.Uint64

type agentModelsSQLKey struct {
	id            uint64
	name          string
	prefix        string
	hash          string
	status        string
	rpm           int
	allowedModels string
	createdAt     time.Time
	updatedAt     time.Time
}

type agentModelsSQLDriver struct {
	keysByHash map[string]agentModelsSQLKey
}

func (d *agentModelsSQLDriver) Open(string) (driver.Conn, error) {
	return &agentModelsSQLConn{keysByHash: d.keysByHash}, nil
}

type agentModelsSQLConn struct {
	keysByHash map[string]agentModelsSQLKey
}

func (c *agentModelsSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported by this test driver")
}

func (c *agentModelsSQLConn) Close() error { return nil }

func (c *agentModelsSQLConn) Begin() (driver.Tx, error) { return agentModelsSQLTx{}, nil }

func (c *agentModelsSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	columns := []string{"id", "name", "prefix", "key_hash", "status", "rpm", "allowed_models", "expires_at", "created_at", "updated_at", "last_used_at"}
	if !strings.Contains(strings.ToLower(query), "from api_keys where key_hash") || len(args) != 1 {
		return &agentModelsSQLRows{columns: columns}, nil
	}
	hash, _ := args[0].Value.(string)
	key, ok := c.keysByHash[hash]
	if !ok {
		return &agentModelsSQLRows{columns: columns}, nil
	}
	values := []driver.Value{
		int64(key.id), key.name, key.prefix, key.hash, key.status, int64(key.rpm),
		key.allowedModels, nil, key.createdAt, key.updatedAt, nil,
	}
	return &agentModelsSQLRows{columns: columns, values: [][]driver.Value{values}}, nil
}

func (c *agentModelsSQLConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	// API Key Touch 与异步审计写入只需要确认执行成功；本测试重点是 HTTP 鉴权/限流/ACL 分支。
	return driver.RowsAffected(1), nil
}

type agentModelsSQLTx struct{}

func (agentModelsSQLTx) Commit() error   { return nil }
func (agentModelsSQLTx) Rollback() error { return nil }

type agentModelsSQLRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *agentModelsSQLRows) Columns() []string { return r.columns }
func (r *agentModelsSQLRows) Close() error      { return nil }

func (r *agentModelsSQLRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

var (
	_ driver.Driver        = (*agentModelsSQLDriver)(nil)
	_ driver.Conn          = (*agentModelsSQLConn)(nil)
	_ driver.QueryerContext = (*agentModelsSQLConn)(nil)
	_ driver.ExecerContext  = (*agentModelsSQLConn)(nil)
	_ driver.Rows           = (*agentModelsSQLRows)(nil)
)

func newAgentModelsSQLKey(raw string, id uint64, rpm int, allowedModels []string) agentModelsSQLKey {
	now := time.Now()
	allowed, _ := json.Marshal(allowedModels)
	return agentModelsSQLKey{
		id: id, name: "agent-models-test", prefix: raw[:8],
		hash: auth.Hash(raw), status: "active", rpm: rpm,
		allowedModels: string(allowed), createdAt: now, updatedAt: now,
	}
}

func newAgentModelsHTTPTestServer(t *testing.T, records []agentModelsSQLKey, limiter *ratelimit.Limiter) (*Server, http.Handler) {
	t.Helper()
	keyMap := make(map[string]agentModelsSQLKey, len(records))
	for _, record := range records {
		keyMap[record.hash] = record
	}
	driverName := fmt.Sprintf("llm-gateway-agent-models-test-%d", agentModelsTestDriverSequence.Add(1))
	sql.Register(driverName, &agentModelsSQLDriver{keysByHash: keyMap})
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatalf("open test SQL database: %v", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	cfg := &config.Config{}
	cfg.Agents = []config.AgentRouteConfig{{
		ID: "deepseek-agent", Enabled: true, PathPrefix: "/deepseek",
		ProviderID: "deepseek", DefaultModel: "deepseek-chat",
	}}
	s := &Server{
		cfg: cfg, db: db, keys: auth.NewRepo(db),
		audit: audit.NewWriter(store.NewRepo(db), 64),
		limiter: limiter,
		webProviders: map[string]managedProvider{
			"deepseek": {ID: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKeyHeader: "authorization", DefaultModel: "deepseek-chat"},
		},
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://127.0.0.1:8080", ModelName: "local.gguf"},
		fileUpstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://127.0.0.1:8080", ModelName: "local.gguf"},
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.ShutdownAudit(ctx); err != nil {
			t.Errorf("shutdown audit writer: %v", err)
		}
		if limiter != nil {
			if err := limiter.Close(); err != nil {
				t.Errorf("close test rate limiter: %v", err)
			}
		}
		if err := db.Close(); err != nil {
			t.Errorf("close test SQL database: %v", err)
		}
	})
	return s, s.Handler()
}

func doAgentModelsRequest(handler http.Handler, rawKey string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "http://gateway.example/deepseek/v1/models?trace=agent-models-test", nil)
	if rawKey != "" {
		request.Header.Set("X-API-Key", rawKey)
	}
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, request)
	return writer
}

func decodeAgentModelsResponse(t *testing.T, body []byte) struct {
	Object string `json:"object"`
	Data []struct {
		ID          string `json:"id"`
		OwnedBy     string `json:"owned_by"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
} {
	t.Helper()
	var payload struct {
		Object string `json:"object"`
		Data []struct {
			ID          string `json:"id"`
			OwnedBy     string `json:"owned_by"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Agent models response: %v, body=%q", err, body)
	}
	return payload
}

func TestAgentModelsHTTPHandlerEnforcesAuthAndModelACL(t *testing.T) {
	const unrestrictedRawKey = "sk-llm-agent-models-unrestricted"
	const restrictedRawKey = "sk-llm-agent-models-restricted"
	limiter := ratelimit.New("127.0.0.1:6379", "", 0) // rpm=0 in these fixtures bypasses Redis I/O.
	s, handler := newAgentModelsHTTPTestServer(t, []agentModelsSQLKey{
		newAgentModelsSQLKey(unrestrictedRawKey, 81001, 0, nil),
		newAgentModelsSQLKey(restrictedRawKey, 81002, 0, []string{"gpt-4.1"}),
	}, limiter)
	_ = s

	if got := doAgentModelsRequest(handler, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("missing API Key status = %d, want 401", got)
	}
	if got := doAgentModelsRequest(handler, "unknown-agent-models-key").Code; got != http.StatusUnauthorized {
		t.Fatalf("unknown API Key status = %d, want 401", got)
	}

	allowed := doAgentModelsRequest(handler, unrestrictedRawKey)
	if allowed.Code != http.StatusOK {
		t.Fatalf("unrestricted Key status = %d, body=%s", allowed.Code, allowed.Body.String())
	}
	allowedPayload := decodeAgentModelsResponse(t, allowed.Body.Bytes())
	if allowedPayload.Object != "list" || len(allowedPayload.Data) != 1 {
		t.Fatalf("unrestricted Key models = %#v, want one fixed Agent model", allowedPayload)
	}
	modelRow := allowedPayload.Data[0]
	if modelRow.ID != "deepseek-chat" || modelRow.OwnedBy != "deepseek" || modelRow.DisplayName != "deepseek-chat" {
		t.Fatalf("Agent model row = %+v, want only the configured DeepSeek target", modelRow)
	}

	restricted := doAgentModelsRequest(handler, restrictedRawKey)
	if restricted.Code != http.StatusOK {
		t.Fatalf("restricted Key status = %d, body=%s", restricted.Code, restricted.Body.String())
	}
	restrictedPayload := decodeAgentModelsResponse(t, restricted.Body.Bytes())
	if restrictedPayload.Object != "list" || len(restrictedPayload.Data) != 0 {
		t.Fatalf("restricted Key models = %#v, want an empty list", restrictedPayload)
	}
}

func TestAgentModelsHTTPHandlerEnforcesRedisRPMLimit(t *testing.T) {
	addr := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDR"))
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; HTTP RPM integration test requires Redis")
	}
	const rawKey = "sk-llm-agent-models-rpm"
	limiter := ratelimit.New(addr, "", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := limiter.Ping(ctx); err != nil {
		cancel()
		_ = limiter.Close()
		t.Fatalf("ping test Redis: %v", err)
	}
	cancel()

	keyID := uint64(time.Now().UnixNano())
	s, handler := newAgentModelsHTTPTestServer(t, []agentModelsSQLKey{
		newAgentModelsSQLKey(rawKey, keyID, 1, nil),
	}, limiter)
	_ = s

	first := doAgentModelsRequest(handler, rawKey)
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, body=%s", first.Code, first.Body.String())
	}
	second := doAgentModelsRequest(handler, rawKey)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, body=%s; want 429", second.Code, second.Body.String())
	}
}
