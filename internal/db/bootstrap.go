// db（bootstrap）：首次部署时用管理员账号自动创建网关专用库和用户，重启可重复执行。
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// BootstrapDatabase 创建/更新网关专用库与应用账号，并授予该库全部权限。
// 设计为幂等：多次启动不会要求手工改 MariaDB。
// BootstrapDatabase creates/updates the dedicated application database and user.
// It is intentionally idempotent so restarting the Gateway does not require
// any manual MariaDB work.
func BootstrapDatabase(ctx context.Context, host string, port int, adminUser, adminPassword, databaseName, appUser, appPassword string) error {
	if !identifierPattern.MatchString(databaseName) {
		return fmt.Errorf("invalid database name: %s", databaseName)
	}
	if !identifierPattern.MatchString(appUser) {
		return fmt.Errorf("invalid database user: %s", appUser)
	}

	adminCfg := mysql.NewConfig()
	adminCfg.User = adminUser
	adminCfg.Passwd = adminPassword
	adminCfg.Net = "tcp"
	adminCfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	adminCfg.DBName = "mysql"
	adminCfg.Params = map[string]string{
		"charset": "utf8mb4",
	}

	adminDB, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open admin mysql: %w", err)
	}
	defer adminDB.Close()

	if err := adminDB.PingContext(ctx); err != nil {
		return fmt.Errorf("admin mysql ping: %w", err)
	}

	quotedDB := "`" + databaseName + "`"
	quotedUser := "`" + strings.ReplaceAll(appUser, "`", "``") + "`"

	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE IF NOT EXISTS `+quotedDB+` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	// MySQL/MariaDB does not treat identifiers as bind parameters, but the names
	// have already been validated above. Password is escaped using the driver's
	// MySQL string escaping rules.
	escapedPassword := escapeMySQLString(appPassword)
	userHost := quotedUser + `@'%'`

	if _, err := adminDB.ExecContext(ctx, `CREATE USER IF NOT EXISTS `+userHost+` IDENTIFIED BY '`+escapedPassword+`'`); err != nil {
		return fmt.Errorf("create database user: %w", err)
	}

	// Keep the configured password authoritative. This only affects the dedicated
	// Gateway account, never the MariaDB admin account.
	if _, err := adminDB.ExecContext(ctx, `ALTER USER `+userHost+` IDENTIFIED BY '`+escapedPassword+`'`); err != nil {
		return fmt.Errorf("set database user password: %w", err)
	}

	if _, err := adminDB.ExecContext(ctx, `GRANT ALL PRIVILEGES ON `+quotedDB+`.* TO `+userHost); err != nil {
		return fmt.Errorf("grant database privileges: %w", err)
	}

	return nil
}

// escapeMySQLString escapes a value placed inside a single-quoted MySQL string literal.
// Passwords are configuration data rather than SQL identifiers, so they are safely
// escaped here before being used in CREATE/ALTER USER statements.
func escapeMySQLString(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 8)
	for _, r := range value {
		switch r {
		case 0:
			b.WriteString("\\0")
		case '\\':
			b.WriteString("\\\\")
		case '\'':
			b.WriteString("\\'")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\x1a':
			b.WriteString("\\Z")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AppDSN 生成网关日常读写用的连接串（含 utf8mb4 与本地时区）。
func AppDSN(host string, port int, databaseName, user, password string) string {
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.DBName = databaseName
	cfg.ParseTime = true
	cfg.Params = map[string]string{
		"charset": "utf8mb4",
		"loc":     "Local",
	}
	return cfg.FormatDSN()
}
