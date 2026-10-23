package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/whl/llm-gateway/internal/audit"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/model"
	"github.com/whl/llm-gateway/internal/ratelimit"
	"github.com/whl/llm-gateway/internal/settings"
	"github.com/whl/llm-gateway/internal/store"
)

// TestGatewayConcurrentRequestLoadWithMariaDBAudit measures latency/throughput
// across the real HTTP handler, Redis limiter, Provider adapter, and asynchronous MariaDB audit writer.
// It is a reproducible integration load sample, not a production-capacity or real-provider benchmark.
func TestGatewayConcurrentRequestLoadWithMariaDBAudit(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	redisAddr := strings.TrimSpace(os.Getenv("REDIS_TEST_ADDR"))
	if dsn == "" || redisAddr == "" {
		t.Skip("MYSQL_TEST_DSN and REDIS_TEST_ADDR are required for the Gateway request-load integration test")
	}

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelSetup()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open MariaDB: %v", err)
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(32)
	if err := db.PingContext(setupCtx); err != nil {
		_ = db.Close()
		t.Fatalf("ping MariaDB: %v", err)
	}

	var databaseName string
	if err := db.QueryRowContext(setupCtx, "SELECT DATABASE()").Scan(&databaseName); err != nil {
		_ = db.Close()
		t.Fatalf("read selected database: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		_ = db.Close()
		t.Fatalf("refusing request load against non-test database %q; database name must end in _test", databaseName)
	}

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		_ = db.Close()
		t.Fatal("resolve test source path to apply the test schema")
	}
	migrationPath := filepath.Join(filepath.Dir(sourceFile), "../../migrations/001_init.sql")
	schema, err := os.ReadFile(migrationPath)
	if err != nil {
		_ = db.Close()
		t.Fatalf("read MariaDB schema %s: %v", migrationPath, err)
	}
	for _, statement := range strings.Split(string(schema), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := db.ExecContext(setupCtx, statement); err != nil {
			_ = db.Close()
			t.Fatalf("apply test schema statement: %v", err)
		}
	}

	limiter := ratelimit.New(redisAddr, "", 0)
	if err := limiter.Ping(setupCtx); err != nil {
		_ = limiter.Close()
		_ = db.Close()
		t.Fatalf("ping test Redis: %v", err)
	}

	var upstreamRequests atomic.Uint64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected upstream path", http.StatusNotFound)
			return
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid request JSON", http.StatusBadRequest)
			return
		}
		var modelID string
		if err := json.Unmarshal(payload["model"], &modelID); err != nil || modelID != "integration-model" {
			http.Error(w, "Gateway did not pin the configured model", http.StatusBadRequest)
			return
		}
		upstreamRequests.Add(1)
		// A small, fixed delay exercises concurrent forwarding without asserting a noisy latency threshold.
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "chatcmpl-loadtest", "object": "chat.completion", "model": modelID,
			"choices": []interface{}{map[string]interface{}{
				"index": 0, "message": map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer upstream.Close()

	const (
		workerCount       = 16
		requestsPerWorker = 12
		totalRequests     = workerCount * requestsPerWorker
	)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	keyName := "gateway-load-" + suffix
	keyRepo := auth.NewRepo(db)
	key, rawKey, err := keyRepo.Create(setupCtx, keyName, 10000, nil, nil)
	if err != nil {
		_ = limiter.Close()
		_ = db.Close()
		t.Fatalf("create temporary integration API key: %v", err)
	}

	cfg := &config.Config{}
	cfg.Llama.Backend = "llamacpp"
	cfg.Llama.AutoLoadOnProxy = false
	cfg.Llama.RequestTimeout = 60
	loggingDisabled := false
	cfg.Logging.Enabled = &loggingDisabled
	cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{
		ID: "loadtest", Enabled: true, BaseURL: upstream.URL + "/v1",
		DefaultModel: "integration-model", APIKey: "test-upstream-key",
		APIKeyHeader: "api-key", AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	}}
	cfg.Agents = []config.AgentRouteConfig{{
		ID: "loadtest-agent", Enabled: true, PathPrefix: "/loadtest",
		ProviderID: "loadtest", DefaultModel: "integration-model",
	}}

	logRepo := store.NewRepo(db)
	srv := &Server{
		cfg: cfg, db: db, keys: keyRepo, models: model.NewRepo(db),
		logs: logRepo, audit: audit.NewWriter(logRepo, 8192), settings: settings.NewRepo(db),
		limiter: limiter, live: live.NewRegistry(), runs: newRunCancels(),
		localClients: make(map[string]localClientEntry),
		upstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://127.0.0.1:8080", ModelName: "unused.gguf"},
		fileUpstream: settings.Upstream{Backend: "llamacpp", BaseURL: "http://127.0.0.1:8080", ModelName: "unused.gguf"},
	}
	srv.pipe = srv.newInferencePipeline()
	handler := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := srv.ShutdownAudit(cleanupCtx); err != nil {
			t.Errorf("shutdown audit writer: %v", err)
		}
		// The unique key name scopes cleanup to this test run only.
		_, _ = db.ExecContext(cleanupCtx, "DELETE FROM request_stream_events WHERE request_id IN (SELECT request_id FROM request_logs WHERE api_key_name=?)", keyName)
		_, _ = db.ExecContext(cleanupCtx, "DELETE FROM request_payloads WHERE request_id IN (SELECT request_id FROM request_logs WHERE api_key_name=?)", keyName)
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM request_logs WHERE api_key_name=?", keyName); err != nil {
			t.Errorf("cleanup request log rows: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, "DELETE FROM api_keys WHERE id=?", key.ID); err != nil {
			t.Errorf("cleanup temporary API key: %v", err)
		}
		if err := limiter.Close(); err != nil {
			t.Errorf("close Redis limiter: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close MariaDB connection: %v", err)
		}
	})
	defer handler.Close()

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns: workerCount * 2, MaxIdleConnsPerHost: workerCount,
			MaxConnsPerHost: workerCount, IdleConnTimeout: 30 * time.Second,
		},
	}
	defer client.CloseIdleConnections()

	body, err := json.Marshal(map[string]interface{}{
		"model": "client-model-must-not-win",
		"messages": []interface{}{map[string]string{"role": "user", "content": "load test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	type requestResult struct {
		elapsed time.Duration
		err     string
	}
	results := make([]requestResult, totalRequests)
	startGate := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func(workerID int) {
			defer workers.Done()
			<-startGate
			for n := 0; n < requestsPerWorker; n++ {
				index := workerID*requestsPerWorker + n
				req, buildErr := http.NewRequest(http.MethodPost, handler.URL+"/loadtest/v1/chat/completions?load_test=1", bytes.NewReader(body))
				if buildErr != nil {
					results[index].err = "build request: " + buildErr.Error()
					continue
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-API-Key", rawKey)
				req.Header.Set("X-Request-ID", fmt.Sprintf("gateway-load-%s-%04d", suffix, index))

				requestStart := time.Now()
				resp, requestErr := client.Do(req)
				if requestErr != nil {
					results[index] = requestResult{elapsed: time.Since(requestStart), err: "HTTP request: " + requestErr.Error()}
					continue
				}
				responseBody, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				results[index].elapsed = time.Since(requestStart)
				if readErr != nil {
					results[index].err = "read response: " + readErr.Error()
				} else if resp.StatusCode != http.StatusOK {
					results[index].err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(responseBody))
				} else if !bytes.Contains(responseBody, []byte("\"content\":\"ok\"")) {
					results[index].err = fmt.Sprintf("unexpected response body: %s", string(responseBody))
				}
			}
		}(worker)
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	loadStart := time.Now()
	close(startGate)
	workers.Wait()
	loadDuration := time.Since(loadStart)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	latenciesMS := make([]float64, 0, totalRequests)
	failures := 0
	for i, result := range results {
		latenciesMS = append(latenciesMS, float64(result.elapsed.Microseconds())/1000)
		if result.err != "" {
			failures++
			if failures <= 10 {
				t.Errorf("request %d failed: %s", i, result.err)
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d/%d concurrent Gateway requests failed", failures, totalRequests)
	}
	if got := upstreamRequests.Load(); got != totalRequests {
		t.Fatalf("mock upstream received %d requests, want %d", got, totalRequests)
	}

	sort.Float64s(latenciesMS)
	percentile := func(p float64) float64 {
		index := int(math.Ceil(p*float64(len(latenciesMS)))) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(latenciesMS) {
			index = len(latenciesMS) - 1
		}
		return latenciesMS[index]
	}
	throughput := float64(totalRequests) / loadDuration.Seconds()
	t.Logf("Gateway HTTP load sample: requests=%d workers=%d duration=%s throughput=%.1f req/s p50=%.2fms p95=%.2fms p99=%.2fms failures=%d upstream_requests=%d",
		totalRequests, workerCount, loadDuration.Round(time.Millisecond), throughput,
		percentile(0.50), percentile(0.95), percentile(0.99), failures, upstreamRequests.Load())
	t.Logf("Go memory sample: heap_alloc_after=%d bytes total_alloc_delta=%d bytes; use /usr/bin/time -v in CI for process CPU time and peak RSS",
		after.HeapAlloc, after.TotalAlloc-before.TotalAlloc)

	// Flush is an ordered barrier: all audit writes enqueued by these HTTP requests must complete before querying.
	drainStart := time.Now()
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 90*time.Second)
	if err := srv.audit.Flush(drainCtx); err != nil {
		cancelDrain()
		t.Fatalf("flush audit queue after load: %v", err)
	}
	cancelDrain()
	drainDuration := time.Since(drainStart)

	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelVerify()
	var persisted, successful, modelMatched int
	err = db.QueryRowContext(verifyCtx,
		"SELECT COUNT(*), COALESCE(SUM(status='success'),0), COALESCE(SUM(model_actual='integration-model'),0) FROM request_logs WHERE api_key_name=?",
		keyName).Scan(&persisted, &successful, &modelMatched)
	if err != nil {
		t.Fatalf("query persisted audit results: %v", err)
	}
	if persisted != totalRequests || successful != totalRequests || modelMatched != totalRequests {
		t.Fatalf("audit rows = %d, successful = %d, correct model = %d; want %d for each", persisted, successful, modelMatched, totalRequests)
	}
	t.Logf("MariaDB audit drain: persisted=%d/%d successful=%d model_matched=%d drain_duration=%s",
		persisted, totalRequests, successful, modelMatched, drainDuration.Round(time.Millisecond))
}
