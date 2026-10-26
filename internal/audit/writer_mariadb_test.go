package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/whl/llm-gateway/internal/store"
)

// TestWriterDrainsMariaDBBacklogAndPreservesAcceptedWrites 对真实 MariaDB 执行请求审计生命周期写入。
// 仅使用专用名称以 _test 结尾的数据库；本地未配置 MYSQL_TEST_DSN 时跳过。
func TestWriterDrainsMariaDBBacklogAndPreservesAcceptedWrites(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MYSQL_TEST_DSN"))
	if dsn == "" {
		t.Skip("MYSQL_TEST_DSN 未设置，跳过 MariaDB 集成压力测试")
	}

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelConnect()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("打开 MariaDB 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)
	if err := db.PingContext(connectCtx); err != nil {
		t.Fatalf("连接 MariaDB 失败: %v", err)
	}

	var databaseName string
	if err := db.QueryRowContext(connectCtx, "SELECT DATABASE()").Scan(&databaseName); err != nil {
		t.Fatalf("读取当前数据库名称失败: %v", err)
	}
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		t.Fatalf("拒绝在非测试数据库 %q 中运行审计压力测试；数据库名称必须以 _test 结尾", databaseName)
	}

	// 字段与 migrations/001_init.sql 中的 request_logs 保持一致，允许测试直接运行在空测试库。
	const createRequestLogsSQL = "CREATE TABLE IF NOT EXISTS request_logs (" +
		"id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY," +
		"request_id VARCHAR(64) NOT NULL," +
		"api_key_id BIGINT UNSIGNED NULL," +
		"api_key_name VARCHAR(128) NULL," +
		"client_ip VARCHAR(64) NULL," +
		"client_name VARCHAR(128) NULL," +
		"user_agent VARCHAR(512) NULL," +
		"endpoint VARCHAR(128) NOT NULL," +
		"request_url VARCHAR(2048) NULL," +
		"method VARCHAR(16) NOT NULL," +
		"model_requested VARCHAR(255) NULL," +
		"model_actual VARCHAR(1024) NULL," +
		"provider_id VARCHAR(64) NULL," +
		"stream TINYINT(1) NOT NULL DEFAULT 0," +
		"status VARCHAR(32) NOT NULL DEFAULT 'running'," +
		"http_status SMALLINT UNSIGNED NULL," +
		"upstream_http_status SMALLINT UNSIGNED NULL," +
		"started_at DATETIME(6) NOT NULL," +
		"finished_at DATETIME(6) NULL," +
		"latency_ms BIGINT UNSIGNED NULL," +
		"ttft_ms BIGINT UNSIGNED NULL," +
		"input_tokens BIGINT UNSIGNED NULL," +
		"output_tokens BIGINT UNSIGNED NULL," +
		"total_tokens BIGINT UNSIGNED NULL," +
		"request_bytes BIGINT UNSIGNED NULL," +
		"response_bytes BIGINT UNSIGNED NULL," +
		"error_code VARCHAR(64) NULL," +
		"error_message TEXT NULL," +
		"analysis_json LONGTEXT NULL," +
		"INDEX idx_req_id (request_id)" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	if _, err := db.ExecContext(connectCtx, createRequestLogsSQL); err != nil {
		t.Fatalf("准备 request_logs 测试表失败: %v", err)
	}

	// CREATE TABLE IF NOT EXISTS 不会修补复用测试库中的旧表；确保字段存在，避免测试依赖执行顺序。
	var providerIDColumnCount int
	if err := db.QueryRowContext(connectCtx, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'request_logs' AND COLUMN_NAME = 'provider_id'`).Scan(&providerIDColumnCount); err != nil {
		t.Fatalf("检查 request_logs.provider_id 字段失败: %v", err)
	}
	if providerIDColumnCount == 0 {
		if _, err := db.ExecContext(connectCtx, "ALTER TABLE request_logs ADD COLUMN provider_id VARCHAR(64) NULL"); err != nil {
			t.Fatalf("补齐 request_logs.provider_id 测试字段失败: %v", err)
		}
	}

	const (
		queueSize         = 2048
		requestCount      = 500
		producerCount     = 10
		normalAttempts    = 400
		criticalAttempts  = 3000
		queuedJobsPerCall = 3 // CreateRequest、PatchRequestModelActual、FinishRequest
	)
	prefix := fmt.Sprintf("audit-test-%d-", time.Now().UnixNano())
	requestIDs := make([]string, requestCount)
	for i := range requestIDs {
		requestIDs[i] = fmt.Sprintf("%s%04d", prefix, i)
	}

	writer := NewWriter(store.NewRepo(db), queueSize)
	releaseWorker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
	t.Cleanup(func() {
		release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := writer.Shutdown(shutdownCtx); err != nil {
			t.Errorf("测试清理阶段关闭审计 Writer 失败: %v", err)
		}
	})

	workerStarted := make(chan struct{})
	writer.enqueue(func(context.Context, *store.Repo) error {
		close(workerStarted)
		<-releaseWorker
		return nil
	})
	select {
	case <-workerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("审计 Worker 未及时开始执行阻塞任务")
	}

	// 暂停数据库 Worker 后，由多个生产者同时投递 500 条完整请求生命周期（共 1500 个真实 DB 操作）。
	var producers sync.WaitGroup
	for producer := 0; producer < producerCount; producer++ {
		start := producer * (requestCount / producerCount)
		end := start + requestCount/producerCount
		producers.Add(1)
		go func(start, end int) {
			defer producers.Done()
			for i := start; i < end; i++ {
				id := requestIDs[i]
				startedAt := time.Now().UTC()
				writer.CreateRequest(store.RequestLog{
					RequestID: id, ClientIP: "127.0.0.1",
					Endpoint: "/v1/chat/completions", Method: "POST",
					ModelRequested: "client-model", Status: "running",
					StartedAt: startedAt, RequestBytes: 1,
				})
				writer.PatchRequestModelActual(id, "integration-model")
				finishedAt := time.Now().UTC()
				writer.FinishRequest(store.RequestLog{
					RequestID: id, Endpoint: "/v1/chat/completions", Method: "POST",
					ModelRequested: "client-model", ModelActual: "integration-model",
					Status: "success", HTTPStatus: 200, UpstreamHTTPStatus: 200,
					StartedAt: startedAt, FinishedAt: &finishedAt,
					LatencyMS: finishedAt.Sub(startedAt).Milliseconds(),
				})
			}
		}(start, end)
	}
	producers.Wait()

	lifecycleJobs := requestCount * queuedJobsPerCall
	if got := len(writer.ch); got != lifecycleJobs {
		t.Fatalf("生命周期任务积压数 = %d，期望 %d", got, lifecycleJobs)
	}

	// 普通分析日志只可使用预留空间以外的容量：队列从 1500 填到 normalLimit。
	for i := 0; i < normalAttempts; i++ {
		writer.SaveRequestAnalysis(requestIDs[i], fmt.Sprintf("{\"iteration\":%d}", i))
	}
	normalAccepted := writer.normalLimit - lifecycleJobs
	if got := len(writer.ch); got != writer.normalLimit {
		t.Fatalf("普通诊断事件入队后队列长度 = %d，期望 %d", got, writer.normalLimit)
	}

	// 关键请求更新可占用剩余预留槽位；随后达到物理队列上限，进一步入队必须被计数丢弃。
	for i := 0; i < criticalAttempts; i++ {
		writer.UpdateRequestBytes(requestIDs[0], int64(i))
	}
	if got := len(writer.ch); got != queueSize {
		t.Fatalf("满载队列长度 = %d，期望 %d", got, queueSize)
	}
	criticalAccepted := queueSize - writer.normalLimit
	expectedDropped := uint64(normalAttempts-normalAccepted + criticalAttempts-criticalAccepted)
	if got := writer.dropped.Load(); got != expectedDropped {
		t.Fatalf("满载丢弃数 = %d，期望 %d", got, expectedDropped)
	}

	release()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelShutdown()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("排空 MariaDB 审计队列失败: %v", err)
	}

	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelVerify()
	var lifecycleCount, completedCount, analyzedCount int
	if err := db.QueryRowContext(verifyCtx,
		"SELECT COUNT(*), COALESCE(SUM(status='success' AND model_actual='integration-model'),0), COALESCE(SUM(analysis_json IS NOT NULL),0) FROM request_logs WHERE request_id LIKE ?",
		prefix+"%").Scan(&lifecycleCount, &completedCount, &analyzedCount); err != nil {
		t.Fatalf("读取 MariaDB 审计结果失败: %v", err)
	}
	if lifecycleCount != requestCount {
		t.Fatalf("MariaDB 持久化请求数 = %d，期望 %d", lifecycleCount, requestCount)
	}
	if completedCount != requestCount {
		t.Fatalf("完成且模型字段正确的审计记录数 = %d，期望 %d", completedCount, requestCount)
	}
	if analyzedCount != normalAccepted {
		t.Fatalf("成功写入分析字段的记录数 = %d，期望 %d", analyzedCount, normalAccepted)
	}

	var finalRequestBytes int64
	if err := db.QueryRowContext(verifyCtx,
		"SELECT request_bytes FROM request_logs WHERE request_id=?", requestIDs[0]).Scan(&finalRequestBytes); err != nil {
		t.Fatalf("读取最终 request_bytes 失败: %v", err)
	}
	if finalRequestBytes != int64(criticalAccepted-1) {
		t.Fatalf("关键更新按 FIFO 排空后的 request_bytes = %d，期望 %d", finalRequestBytes, criticalAccepted-1)
	}

	t.Logf("MariaDB 实际写入验收通过：%d 条完整请求生命周期，%d 条分析更新，队列峰值 %d，满载丢弃 %d，队列关闭后排空完成",
		requestCount, analyzedCount, queueSize, expectedDropped)
}
