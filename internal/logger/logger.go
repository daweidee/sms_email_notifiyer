package logger

import (
	"log/slog"
	"os"
	"strings"
)

// Init 根据配置的日志等级初始化全局 logger。
func Init(level string) {
	lvl := parseLevel(level)
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: lvl,
	})
	slog.SetDefault(slog.New(handler))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

