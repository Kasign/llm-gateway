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
	"gateway_settings",
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
	alters := []string{
		`ALTER TABLE models ADD COLUMN runtime_json LONGTEXT NULL`,
		`ALTER TABLE request_logs ADD COLUMN client_name VARCHAR(128) NULL`,
		`ALTER TABLE request_logs ADD COLUMN ttft_ms BIGINT UNSIGNED NULL`,
		`ALTER TABLE request_logs ADD COLUMN analysis_json LONGTEXT NULL`,
		`ALTER TABLE request_logs ADD COLUMN request_url VARCHAR(2048) NULL`,
		`ALTER TABLE request_payloads ADD COLUMN request_headers_json LONGTEXT NULL`,
		`ALTER TABLE request_payloads ADD COLUMN upstream_headers_json LONGTEXT NULL`,
	}
	for _, sqlText := range alters {
		if _, err := db.ExecContext(ctx, sqlText); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("schema upgrade failed: %w", err)
		}
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
