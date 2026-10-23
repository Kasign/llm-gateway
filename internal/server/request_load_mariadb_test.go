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
	if strings.TrimSpace(os.Getenv("GATEWAY_LOAD_TEST")) != "1" {
		t.Skip("set GATEWAY_LOAD_TEST=1 to run the timed Gateway request-load sample")
	}
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
		switch r.URL.Query().Get("fault") {
		case "delay":
			// 受控延迟用于检查故障下的真实端到端等待，而不是模拟真实模型推理。
			time.Sleep(75 * time.Millisecond)
		case "rate_limit":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": "injected upstream secret details"},
			})
			return
		case "unavailable":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{"message": "injected upstream secret details"},
			})
			return
		default:
			// 正常路径固定延迟 2ms，方便和故障场景对照。
			time.Sleep(2 * time.Millisecond)
		}
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
	key, rawKey, err := keyRepo.Create(setupCtx, keyName, 30000, nil, nil)
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
		logs: logRepo, audit: audit.NewWriter(logRepo, 16384), settings: settings.NewRepo(db),
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
		elapsed   time.Duration
		err       string
		statusCode int
	}
	doRequestExpected := func(index int, fault string, expectedStatus int) requestResult {
		requestURL := handler.URL + "/loadtest/v1/chat/completions?load_test=1"
		if fault != "" {
			requestURL += "&fault=" + fault
		}
		req, buildErr := http.NewRequest(http.MethodPost, requestURL, bytes.NewReader(body))
		if buildErr != nil {
			return requestResult{err: "build request: " + buildErr.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", rawKey)
		req.Header.Set("X-Request-ID", fmt.Sprintf("gateway-load-%s-%04d", suffix, index))

		requestStart := time.Now()
		resp, requestErr := client.Do(req)
		if requestErr != nil {
			return requestResult{elapsed: time.Since(requestStart), err: "HTTP request: " + requestErr.Error()}
		}
		responseBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		result := requestResult{elapsed: time.Since(requestStart), statusCode: resp.StatusCode}
		if readErr != nil {
			result.err = "read response: " + readErr.Error()
		} else if resp.StatusCode != expectedStatus {
			result.err = fmt.Sprintf("HTTP %d, want %d: %s", resp.StatusCode, expectedStatus, string(responseBody))
		} else if expectedStatus == http.StatusOK {
			if !bytes.Contains(responseBody, []byte("\"content\":\"ok\"")) {
				result.err = fmt.Sprintf("unexpected success response body: %s", string(responseBody))
			}
		} else {
			// OpenAI-compatible 上游非 2xx 应保留状态码，但错误正文必须脱敏。
			if !bytes.Contains(responseBody, []byte("\"error\"")) {
				result.err = fmt.Sprintf("expected safe JSON error envelope: %s", string(responseBody))
			}
			if bytes.Contains(responseBody, []byte("injected upstream secret details")) {
				result.err = "upstream error details leaked into Gateway response"
			}
			if fault == "rate_limit" && resp.Header.Get("Retry-After") != "1" {
				result.err = fmt.Sprintf("Retry-After header = %q, want 1", resp.Header.Get("Retry-After"))
			}
		}
		return result
	}
	doRequest := func(index int) requestResult {
		return doRequestExpected(index, "", http.StatusOK)
	}

	// 故障注入独立运行，复用相同真实 Handler/Redis/MariaDB/Provider fixture，
	// 避免每次验证都重复执行 60 秒稳态负载。
	if strings.TrimSpace(os.Getenv("GATEWAY_FAULT_TEST")) == "1" {
		const (
			faultRequestCount = 120
			faultWorkerCount  = 16
		)
		type faultScenario struct {
			name   string
			query  string
			status int
		}
		scenarios := []faultScenario{
			{name: "normal", status: http.StatusOK},
			{name: "delay", query: "delay", status: http.StatusOK},
			{name: "429", query: "rate_limit", status: http.StatusTooManyRequests},
			{name: "503", query: "unavailable", status: http.StatusServiceUnavailable},
		}
		results := make([]requestResult, faultRequestCount)
		jobQueue := make(chan int)
		var workers sync.WaitGroup
		workers.Add(faultWorkerCount)
		for worker := 0; worker < faultWorkerCount; worker++ {
			go func() {
				defer workers.Done()
				for index := range jobQueue {
					scenario := scenarios[index%len(scenarios)]
					results[index] = doRequestExpected(index, scenario.query, scenario.status)
				}
			}()
		}
		faultStart := time.Now()
		for index := 0; index < faultRequestCount; index++ {
			jobQueue <- index
		}
		close(jobQueue)
		workers.Wait()
		faultDuration := time.Since(faultStart)

		var failures int
		var delayLatencies, normalLatencies []float64
		var count200, count429, count503 int
		for index, result := range results {
			scenario := scenarios[index%len(scenarios)]
			if result.err != "" {
				failures++
				if failures <= 10 {
					t.Errorf("fault request %d (%s) failed: %s", index, scenario.name, result.err)
				}
			}
			switch result.statusCode {
			case http.StatusOK:
				count200++
			case http.StatusTooManyRequests:
				count429++
			case http.StatusServiceUnavailable:
				count503++
			}
			latencyMS := float64(result.elapsed.Microseconds()) / 1000
			if scenario.name == "delay" {
				delayLatencies = append(delayLatencies, latencyMS)
			} else if scenario.name == "normal" {
				normalLatencies = append(normalLatencies, latencyMS)
			}
		}
		if failures > 0 {
			t.Fatalf("%d/%d injected fault/control requests failed their HTTP/body contract", failures, faultRequestCount)
		}
		if got := upstreamRequests.Load(); got != faultRequestCount {
			t.Fatalf("fault injection upstream request count = %d, want %d", got, faultRequestCount)
		}
		if count200 != 60 || count429 != 30 || count503 != 30 {
			t.Fatalf("fault injection status counts: 200=%d 429=%d 503=%d; want 60/30/30", count200, count429, count503)
		}
		sort.Float64s(delayLatencies)
		sort.Float64s(normalLatencies)
		delayP50 := percentile(delayLatencies, 0.50)
		delayP95 := percentile(delayLatencies, 0.95)
		normalP50 := percentile(normalLatencies, 0.50)
		if delayP50 < 70 {
			t.Fatalf("controlled 75ms upstream delay P50 = %.2fms, expected at least 70ms", delayP50)
		}

		// 等待所有异步审计真正写入数据库，再断言 Gateway 与上游状态、排空行为。
		drainStart := time.Now()
		drainCtx, cancelDrain := context.WithTimeout(context.Background(), 90*time.Second)
		if err := srv.audit.Flush(drainCtx); err != nil {
			cancelDrain()
			t.Fatalf("flush audit after fault injection: %v", err)
		}
		cancelDrain()
		drainDuration := time.Since(drainStart)

		verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelVerify()
		var persisted, client200, client429, upstream429, client503, upstream503 int
		err := db.QueryRowContext(verifyCtx,
			"SELECT COUNT(*), COALESCE(SUM(http_status=200),0), COALESCE(SUM(http_status=429),0), COALESCE(SUM(upstream_http_status=429),0), COALESCE(SUM(http_status=503),0), COALESCE(SUM(upstream_http_status=503),0) FROM request_logs WHERE api_key_name=?",
			keyName).Scan(&persisted, &client200, &client429, &upstream429, &client503, &upstream503)
		if err != nil {
			t.Fatalf("query persisted fault audit results: %v", err)
		}
		if persisted != faultRequestCount || client200 != 60 || client429 != 30 || upstream429 != 30 || client503 != 30 || upstream503 != 30 {
			t.Fatalf("fault audit rows=%d status counts gateway(200/429/503)=%d/%d/%d upstream(429/503)=%d/%d",
				persisted, client200, client429, client503, upstream429, upstream503)
		}
		t.Logf("Gateway upstream fault injection: requests=%d concurrency=%d duration=%s failures=%d statuses(200/429/503)=%d/%d/%d normal_p50=%.2fms delayed_p50=%.2fms delayed_p95=%.2fms audit_rows=%d drain=%s",
			faultRequestCount, faultWorkerCount, faultDuration.Round(time.Millisecond), failures,
			count200, count429, count503, normalP50, delayP50, delayP95, persisted, drainDuration.Round(time.Millisecond))
		return
	}

	// 第一阶段：16 并发突发样本，便于和之前的基线对比。
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
				results[index] = doRequest(index)
			}
		}(worker)
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	loadStart := time.Now()
	close(startGate)
	workers.Wait()
	loadDuration := time.Since(loadStart)

	burstLatenciesMS := make([]float64, 0, totalRequests)
	burstFailures := 0
	for i, result := range results {
		burstLatenciesMS = append(burstLatenciesMS, float64(result.elapsed.Microseconds())/1000)
		if result.err != "" {
			burstFailures++
			if burstFailures <= 10 {
				t.Errorf("burst request %d failed: %s", i, result.err)
			}
		}
	}
	if burstFailures > 0 {
		t.Fatalf("%d/%d burst Gateway requests failed", burstFailures, totalRequests)
	}
	if got := upstreamRequests.Load(); got != totalRequests {
		t.Fatalf("after burst, mock upstream received %d requests, want %d", got, totalRequests)
	}

	percentile := func(values []float64, p float64) float64 {
		index := int(math.Ceil(p*float64(len(values)))) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(values) {
			index = len(values) - 1
		}
		return values[index]
	}
	sort.Float64s(burstLatenciesMS)
	burstThroughput := float64(totalRequests) / loadDuration.Seconds()
	t.Logf("Gateway burst sample: requests=%d workers=%d duration=%s throughput=%.1f req/s p50=%.2fms p95=%.2fms p99=%.2fms failures=%d upstream_requests=%d",
		totalRequests, workerCount, loadDuration.Round(time.Millisecond), burstThroughput,
		percentile(burstLatenciesMS, 0.50), percentile(burstLatenciesMS, 0.95), percentile(burstLatenciesMS, 0.99),
		burstFailures, upstreamRequests.Load())

	// 第二阶段：以 200 请求/秒目标速率持续 60 秒，测量稳态下的请求延迟与审计积压。
	const (
		sustainedRequestCount = 12000
		sustainedInterval     = 5 * time.Millisecond
	)
	sustainedResults := make([]requestResult, sustainedRequestCount)
	sustainedSlots := make(chan struct{}, workerCount)
	var sustainedWorkers sync.WaitGroup
	sustainedStart := time.Now()
	for i := 0; i < sustainedRequestCount; i++ {
		scheduledAt := sustainedStart.Add(time.Duration(i) * sustainedInterval)
		if wait := time.Until(scheduledAt); wait > 0 {
			time.Sleep(wait)
		}
		// 生成器遇到并发上限时会等待可用槽位，并把等待时间计入总持续负载时长。
		sustainedSlots <- struct{}{}
		sustainedWorkers.Add(1)
		index := totalRequests + i
		go func(resultIndex, requestIndex int) {
			defer sustainedWorkers.Done()
			defer func() { <-sustainedSlots }()
			sustainedResults[resultIndex] = doRequest(requestIndex)
		}(i, index)
	}
	sustainedWorkers.Wait()
	sustainedDuration := time.Since(sustainedStart)

	sustainedLatenciesMS := make([]float64, 0, sustainedRequestCount)
	sustainedFailures := 0
	for i, result := range sustainedResults {
		sustainedLatenciesMS = append(sustainedLatenciesMS, float64(result.elapsed.Microseconds())/1000)
		if result.err != "" {
			sustainedFailures++
			if sustainedFailures <= 10 {
				t.Errorf("sustained request %d failed: %s", i, result.err)
			}
		}
	}
	if sustainedFailures > 0 {
		t.Fatalf("%d/%d sustained Gateway requests failed", sustainedFailures, sustainedRequestCount)
	}
	expectedTotalRequests := totalRequests + sustainedRequestCount
	if got := upstreamRequests.Load(); got != uint64(expectedTotalRequests) {
		t.Fatalf("mock upstream received %d requests, want %d total across burst and sustained phases", got, expectedTotalRequests)
	}
	sort.Float64s(sustainedLatenciesMS)
	sustainedThroughput := float64(sustainedRequestCount) / sustainedDuration.Seconds()
	t.Logf("Gateway sustained sample: requests=%d target_rate=%.1f req/s workers_cap=%d duration=%s achieved_throughput=%.1f req/s p50=%.2fms p95=%.2fms p99=%.2fms failures=%d total_upstream_requests=%d",
		sustainedRequestCount, float64(time.Second)/float64(sustainedInterval), workerCount,
		sustainedDuration.Round(time.Millisecond), sustainedThroughput,
		percentile(sustainedLatenciesMS, 0.50), percentile(sustainedLatenciesMS, 0.95), percentile(sustainedLatenciesMS, 0.99),
		sustainedFailures, upstreamRequests.Load())

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("Go memory sample across both phases: heap_alloc_after=%d bytes total_alloc_delta=%d bytes; use /usr/bin/time -v in CI for process CPU time and peak RSS",
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
	// expectedTotalRequests 已覆盖突发与稳态两个阶段。

	if persisted != expectedTotalRequests || successful != expectedTotalRequests || modelMatched != expectedTotalRequests {
		t.Fatalf("audit rows = %d, successful = %d, correct model = %d; want %d for each", persisted, successful, modelMatched, expectedTotalRequests)
	}
	t.Logf("MariaDB audit drain after burst + sustained load: persisted=%d/%d successful=%d model_matched=%d drain_duration=%s",
		persisted, expectedTotalRequests, successful, modelMatched, drainDuration.Round(time.Millisecond))
}
