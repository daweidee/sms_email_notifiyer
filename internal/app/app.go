package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"hello/internal/api"
	"hello/internal/config"
	"hello/internal/db"
	"hello/internal/email"
	"hello/internal/logger"
)

// Run 是应用程序的入口，负责初始化配置、数据库和 HTTP 服务。
func Run(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	// 初始化全局日志
	logger.Init(cfg.Log.Level)

	// 基础配置检查
	if cfg.Server.Port <= 0 {
		return fmt.Errorf("配置错误: server.port 必须大于 0")
	}
	if cfg.DB.DSN == "" {
		return fmt.Errorf("配置错误: db.dsn 不能为空")
	}

	slog.Info("配置检查通过",
		slog.String("log_level", cfg.Log.Level),
		slog.String("server_host", cfg.Server.Host),
		slog.Int("server_port", cfg.Server.Port),
	)

	dbConn, err := db.NewConnection(cfg.DB)
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	defer dbConn.Close()

	slog.Info("数据库连接成功")

	sender := email.NewSender(cfg.Email, dbConn)

	mux := http.NewServeMux()
	defaultFrom := cfg.Email.DefaultFrom()
	restCfg := cfg.Email.DefaultRestConfig()
	apiServer := api.NewServer(sender, dbConn, defaultFrom, restCfg, cfg.SMS)
	apiServer.RegisterRoutes(mux)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 在单独的 goroutine 中启动 HTTP 服务
	errChan := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务启动", slog.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	select {
	case <-ctx.Done():
		// 优雅关闭
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("服务关闭失败: %w", err)
		}
		slog.Info("HTTP 服务已优雅退出")
		return nil
	case err := <-errChan:
		return fmt.Errorf("HTTP 服务异常退出: %w", err)
	}
}

