package obs

import (
	"context"
	"log/slog"
)

type loggerKey struct{}

// ContextWithLogger 把 request 级 logger 放入 ctx（中间件注入）。
func ContextWithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// LoggerFrom 取出 ctx 中的 logger；缺失时回退默认 logger（字段为 "-" 基线）。
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
