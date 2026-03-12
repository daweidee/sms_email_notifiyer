package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hello/internal/app"
)

func main() {
	cfgPath := flag.String("c", "config.yaml", "配置文件路径")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, *cfgPath); err != nil {
		slog.Error("应用退出，错误", slog.Any("err", err))
		// 给日志刷盘一点时间
		time.Sleep(200 * time.Millisecond)
		os.Exit(1)
	}
}
