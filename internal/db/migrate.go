// db（migrate）：执行 SQL 迁移脚本，并补齐后续版本新增的表/列（兼容老库）。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
)

// requiredTables 与 migrations/001_init.sql 及 ensureColumns 中创建的表一致。
var requiredTables = []string{
	"api_keys",
	"models",
	"request_logs",
	"request_payloads",
	"request_stream_events",
	"request_tool_call_events",
	"gateway_settings",
	"model_prices",
	"benchmark_tasks",
	"benchmark_runs",
}

// Migrate 按分号拆分执行迁移文件，再跑 ensureColumns 做增量结构升级，并校验必需表存在。
func Migrate(ctx context.Context, db *sql.DB, host string, port int, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if port <= 0 {
		port = 3306
	}
	host = strings.TrimSpace(host)
	if host == "" {
		host = "?"
	}
	applog.Infof("database migration: target %s:%d applying %s", host, port, path)
	var stmtCount int
	for _, stmt := range strings.Split(string(data), ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if table := parseCreateTableName(stmt); table != "" {
			applog.Infof("database migration: CREATE TABLE %s", table)
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration failed (%s): %w", summarizeStmt(stmt), err)
		}
		stmtCount++
	}
	applog.Infof("database migration: executed %d statement(s) from file", stmtCount)
	if err := ensureColumns(ctx, db); err != nil {
		return err
	}
	if err := verifyRequiredTables(ctx, db); err != nil {
		return err
	}
	schema, _ := currentDatabase(ctx, db)
	applog.Infof("database migration ok: %s:%d schema %q verified %d table(s)", host, port, schema, len(requiredTables))
	return nil
}

func parseCreateTableName(stmt string) string {
	fields := strings.Fields(strings.ToLower(stmt))
	if len(fields) < 3 || fields[0] != "create" || fields[1] != "table" {
		return ""
	}
	i := 2
	if i+2 < len(fields) && fields[i] == "if" && fields[i+1] == "not" && fields[i+2] == "exists" {
		i += 3
	}
	if i >= len(fields) {
		return ""
	}
	name := fields[i]
	name = strings.Trim(name, "`")
	if idx := strings.IndexByte(name, '('); idx >= 0 {
		name = name[:idx]
	}
	return name
}

func summarizeStmt(stmt string) string {
	s := strings.Join(strings.Fields(stmt), " ")
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func currentDatabase(ctx context.Context, db *sql.DB) (string, error) {
	var name sql.NullString
	err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&name)
	if err != nil {
		return "", err
	}
	if !name.Valid {
		return "", nil
	}
	return name.String, nil
}

func verifyRequiredTables(ctx context.Context, db *sql.DB) error {
	var missing []string
	for _, table := range requiredTables {
		var found string
		err := db.QueryRowContext(ctx, `
SELECT TABLE_NAME FROM information_schema.TABLES
WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&found)
		if err == sql.ErrNoRows {
			missing = append(missing, table)
			continue
		}
		if err != nil {
			return fmt.Errorf("verify table %s: %w", table, err)
		}
		applog.Infof("database migration: table %s exists", table)
	}
	if len(missing) > 0 {
		schema, _ := currentDatabase(ctx, db)
		return fmt.Errorf("required table(s) missing in schema %q: %s", schema, strings.Join(missing, ", "))
	}
	return nil
}

// ensureColumns 在已有部署上追加新字段/表；列已存在时忽略 duplicate column 错误。
func ensureColumns(ctx context.Context, db *sql.DB) error {
	applog.Infof("database migration: applying schema upgrades (ensureColumns)")
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS gateway_settings (
  key_name VARCHAR(64) NOT NULL PRIMARY KEY,
  value_json LONGTEXT NOT NULL,
  updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("schema upgrade failed (gateway_settings): %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS benchmark_tasks (
  task_id VARCHAR(64) NOT NULL PRIMARY KEY,
  api_key_id BIGINT UNSIGNED NULL,
  scenario VARCHAR(32) NOT NULL DEFAULT 'performance',
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  provider_id VARCHAR(64) NULL,
  backend VARCHAR(32) NULL,
  model_key VARCHAR(128) NULL,
  model_id VARCHAR(1024) NULL,
  prompt_sha256 CHAR(64) NULL,
  prompt_length BIGINT UNSIGNED NOT NULL DEFAULT 0,
  max_tokens INT UNSIGNED NOT NULL DEFAULT 0,
  config_json LONGTEXT NULL,
  run_count INT UNSIGNED NOT NULL DEFAULT 0,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  latency_ms BIGINT UNSIGNED NULL,
  error_code VARCHAR(64) NULL,
  error_message VARCHAR(512) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  INDEX idx_benchmark_tasks_status_started (status, started_at),
  INDEX idx_benchmark_tasks_key_started (api_key_id, started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS benchmark_runs (
  run_id VARCHAR(64) NOT NULL PRIMARY KEY,
  task_id VARCHAR(64) NOT NULL,
  run_no INT UNSIGNED NOT NULL,
  request_id VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  provider_id VARCHAR(64) NULL,
  model_key VARCHAR(128) NULL,
  model_id VARCHAR(1024) NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  latency_ms BIGINT UNSIGNED NULL,
  model_load_ms BIGINT UNSIGNED NULL,
  inference_latency_ms BIGINT UNSIGNED NULL,
  http_status SMALLINT UNSIGNED NULL,
  response_bytes BIGINT UNSIGNED NULL,
  input_tokens BIGINT UNSIGNED NULL,
  output_tokens BIGINT UNSIGNED NULL,
  total_tokens BIGINT UNSIGNED NULL,
  ttft_ms BIGINT UNSIGNED NULL,
  stream_event_count INT UNSIGNED NOT NULL DEFAULT 0,
  reasoning_chars INT UNSIGNED NOT NULL DEFAULT 0,
  content_chars INT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NULL,
  error_message VARCHAR(512) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_benchmark_run_task_no (task_id, run_no),
  UNIQUE KEY uk_benchmark_run_request (request_id),
  INDEX idx_benchmark_runs_task_status (task_id, status),
  CONSTRAINT fk_benchmark_runs_task FOREIGN KEY (task_id)
    REFERENCES benchmark_tasks(task_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`); err != nil {
		return fmt.Errorf("schema upgrade failed (benchmark_tasks/runs): %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS model_prices (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  provider_id VARCHAR(64) NOT NULL,
  model_id VARCHAR(255) NOT NULL,
  input_price_per_million DECIMAL(24,12) NOT NULL,
  output_price_per_million DECIMAL(24,12) NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'USD',
  effective_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_model_price_effective (provider_id, model_id, effective_at), INDEX idx_model_price_lookup (provider_id, model_id, effective_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("schema upgrade failed (model_prices): %w", err)
	}
	alters := []string{
		`ALTER TABLE api_keys ADD COLUMN allowed_tools TEXT NULL`,
		`ALTER TABLE models ADD COLUMN runtime_json LONGTEXT NULL`,
		`ALTER TABLE request_logs ADD COLUMN client_name VARCHAR(128) NULL`,
		`ALTER TABLE request_logs ADD COLUMN ttft_ms BIGINT UNSIGNED NULL`,
		`ALTER TABLE request_logs ADD COLUMN analysis_json LONGTEXT NULL`,
		`ALTER TABLE request_logs ADD COLUMN token_source VARCHAR(32) NULL`,
		`ALTER TABLE request_logs ADD COLUMN request_url VARCHAR(2048) NULL`,
		`ALTER TABLE request_logs ADD COLUMN provider_id VARCHAR(64) NULL`,
		`ALTER TABLE request_logs ADD INDEX idx_provider_started (provider_id, started_at)`,
		`ALTER TABLE request_logs ADD COLUMN upstream_http_status SMALLINT UNSIGNED NULL`,
		`ALTER TABLE request_payloads ADD COLUMN request_headers_json LONGTEXT NULL`,
		`ALTER TABLE request_payloads ADD COLUMN upstream_headers_json LONGTEXT NULL`,
	}
	for _, sqlText := range alters {
		if _, err := db.ExecContext(ctx, sqlText); err != nil {
			lowerErr := strings.ToLower(err.Error())
			// MariaDB 对已存在列和已存在索引分别返回 duplicate column / duplicate key name。
			if strings.Contains(lowerErr, "duplicate column") || strings.Contains(lowerErr, "duplicate key name") {
				continue
			}
			return fmt.Errorf("schema upgrade failed: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS request_tool_call_events (
  request_id VARCHAR(64) NOT NULL,
  seq SMALLINT UNSIGNED NOT NULL,
  round_no TINYINT UNSIGNED NOT NULL,
  tool_name VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL,
  duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
  result_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (request_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("schema upgrade failed (request_tool_call_events): %w", err)
	}
	applog.Infof("database migration: CREATE TABLE IF NOT EXISTS request_stream_events (ensureColumns)")
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS request_stream_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  request_id VARCHAR(64) NOT NULL,
  seq INT NOT NULL,
  at_ms BIGINT UNSIGNED NOT NULL,
  kind VARCHAR(32) NOT NULL,
  field_name VARCHAR(32) NULL,
  delta_len INT UNSIGNED NULL,
  preview VARCHAR(512) NULL,
  raw_line TEXT NULL,
  INDEX idx_req_stream (request_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return fmt.Errorf("schema upgrade failed: %w", err)
	}
	streamAlters := []string{
		`ALTER TABLE request_stream_events ADD COLUMN raw_line TEXT NULL`,
	}
	for _, sqlText := range streamAlters {
		if _, err := db.ExecContext(ctx, sqlText); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("schema upgrade failed: %w", err)
		}
	}
	return nil
}
