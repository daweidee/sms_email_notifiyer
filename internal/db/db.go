package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"hello/internal/config"
)

// NewConnection 根据配置创建数据库连接，并带有连接超时和连通性检查。
func NewConnection(cfg config.DBConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	if cfg.MaxConn > 0 {
		db.SetMaxOpenConns(cfg.MaxConn)
	}
	if cfg.MaxIdle > 0 {
		db.SetMaxIdleConns(cfg.MaxIdle)
	}

	timeout := time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("数据库连通性检查失败(超时 %s): %w", timeout, err)
	}

	return db, nil
}

