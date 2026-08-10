package obs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// initLogExport 建立 stdout JSON + OTLP 双出口日志（Loki 侧三信号之一）。
// 返回 fanout 后的最终 handler。
func (p *Provider) initLogExport(ctx context.Context, cfg Config, stdout slog.Handler) slog.Handler {
	exp, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(cfg.OTLPEndpoint),
		otlploghttp.WithInsecure(),
		otlploghttp.WithTimeout(3*time.Second),
	)
	if err != nil {
		slog.New(stdout).Warn("log export init failed, stdout only", "err", err)
		return stdout
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(newResource(cfg.Component)),
	)
	p.shutdown = append(p.shutdown, lp.Shutdown)
	otelHandler := otelslog.NewHandler("airush/"+cfg.Component, otelslog.WithLoggerProvider(lp))
	return fanoutHandler{stdout, otelHandler}
}

// fanoutHandler 把日志同时写入多个 handler（自研 ~30 行，免引 slog-multi 依赖）。
type fanoutHandler []slog.Handler

func (f fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("fanout handle: %w", err)
			}
		}
	}
	return firstErr
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(fanoutHandler, len(f))
	for i, h := range f {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	next := make(fanoutHandler, len(f))
	for i, h := range f {
		next[i] = h.WithGroup(name)
	}
	return next
}
