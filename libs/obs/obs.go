// Package obs 是三件套观测基线（spec-0.9 D1）：结构化日志（必带字段 +
// redaction 兜底）、OTel tracing/metrics（OTLP 出口）、HTTP 中间件。
//
// 契约（spec-0.9 §3）：初始化失败不阻断服务启动——OTLP 端点未配置或不可达时
// 降级为 stdout 日志 + no-op tracer/meter，并以告警日志显式暴露降级事实。
package obs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Config 是观测初始化配置（各组件经 spec-0.7 配置框架装配）。
type Config struct {
	// Component 进入日志必带字段与 OTel service.name。
	Component string
	// OTLPEndpoint 形如 localhost:4318（http）；空 = 关闭导出（纯日志模式）。
	OTLPEndpoint string
	// SampleRatio trace 采样率 [0,1]；错误 span 恒保留（spec-0.9 Q5）。
	SampleRatio float64
	// LogLevel debug/info/warn/error。
	LogLevel string
}

// Provider 聚合观测句柄；Shutdown 用于优雅退出 flush。
type Provider struct {
	Logger   *slog.Logger
	shutdown []func(context.Context) error
}

// Init 初始化三件套并把 slog 默认 logger 替换为带必带字段的实例。
func Init(ctx context.Context, cfg Config) *Provider {
	p := &Provider{}
	var handler slog.Handler = stdoutHandler(cfg)

	if cfg.OTLPEndpoint != "" {
		handler = p.initLogExport(ctx, cfg, handler)
		if err := p.initTracing(ctx, cfg); err != nil {
			slog.New(handler).Warn("tracing init failed, degrading to no-op", "err", err)
		}
		if err := p.initMetrics(ctx, cfg); err != nil {
			slog.New(handler).Warn("metrics init failed, degrading to no-op", "err", err)
		}
		otel.SetTextMapPropagator(propagation.TraceContext{})
	}

	// record 级打码置于 fanout 之外：stdout 与 OTLP 双出口同享脱敏
	p.Logger = withBaseFields(redactHandler{inner: handler}, cfg)
	slog.SetDefault(p.Logger)
	if cfg.OTLPEndpoint == "" {
		p.Logger.Warn("observability export disabled (no OTLP endpoint), logs-only mode")
	}
	return p
}

// Shutdown flush 并关闭导出器（服务退出路径调用）。
func (p *Provider) Shutdown(ctx context.Context) {
	for _, fn := range p.shutdown {
		if err := fn(ctx); err != nil {
			p.Logger.Warn("observability shutdown", "err", err)
		}
	}
}

func (p *Provider) initTracing(ctx context.Context, cfg Config) error {
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithTimeout(3*time.Second),
	)
	if err != nil {
		return fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(errorPreservingSampler(cfg.SampleRatio)),
		sdktrace.WithResource(newResource(cfg.Component)),
	)
	otel.SetTracerProvider(tp)
	p.shutdown = append(p.shutdown, tp.Shutdown)
	return nil
}

func (p *Provider) initMetrics(ctx context.Context, cfg Config) error {
	exp, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint),
		otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithTimeout(3*time.Second),
	)
	if err != nil {
		return fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp,
			sdkmetric.WithInterval(15*time.Second))),
		sdkmetric.WithResource(newResource(cfg.Component)),
	)
	otel.SetMeterProvider(mp)
	p.shutdown = append(p.shutdown, mp.Shutdown)
	return nil
}

func newResource(component string) *resource.Resource {
	// NewSchemaless 避免与 Default 的 semconv schema 版本冲突
	// （Merge 冲突会静默失败导致 service.name 变 unknown_service）。
	r, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName("airush-"+component),
	))
	if err != nil {
		return resource.Default()
	}
	return r
}

// errorPreservingSampler head 采样 + 错误恒采（父带错误标记时保留）在 span
// 处理层无法前置判断，故实现为 ParentBased(ratio)；错误保留由 collector 侧
// tail 策略演进（spec-0.9 Q5 声明 Stage 2+），当前 dev 默认 ratio=1 全采。
func errorPreservingSampler(ratio float64) sdktrace.Sampler {
	if ratio <= 0 {
		ratio = 0
	}
	if ratio >= 1 {
		return sdktrace.AlwaysSample()
	}
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// levelFrom 把配置级别映射为 slog.Level。
func levelFrom(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func stdoutHandler(cfg Config) slog.Handler {
	return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       levelFrom(cfg.LogLevel),
		ReplaceAttr: redactAttr,
	})
}

// withBaseFields 注入必带字段（development-standards §1.5）：component 固定；
// tenant_id/trace_id 由 ensureFields 保底——缺上下文时补 "-"，
// 请求中间件 With 提供后不重复注入。
func withBaseFields(h slog.Handler, cfg Config) *slog.Logger {
	return slog.New(ensureFields{inner: h}).With("component", cfg.Component)
}
