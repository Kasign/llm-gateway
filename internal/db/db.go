// db 封装 MySQL/MariaDB 连接：打开连接池、Ping 校验，供网关持久化模型与请求日志。
package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// DB 是对标准库 sql.DB 的薄包装，方便在 main 里统一 Close。
type DB struct{ SQL *sql.DB }

// Open 建立应用账号的数据库连接并限制连接池大小，避免占满数据库。
func Open(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("mysql ping: %w", err)
	}
	return &DB{SQL: sqlDB}, nil
}

func (d *DB) Close() error { return d.SQL.Close() }
