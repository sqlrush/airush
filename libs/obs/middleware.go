package obs

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// httpMetrics 惰性构造（Init 之后首次请求时创建，绑定当时的全局 MeterProvider）。
type httpMetrics struct {
	requests metric.Int64Counter
	duration metric.Float64Histogram
}

// HTTPMiddleware 服务端观测中间件（spec-0.9 D1）：
// 提取/生成 trace context、注入带 trace_id 的 request logger、记录请求指标。
// 对业务 handler 透明：不吞错、不改响应（spec-0.9 §3）。
func HTTPMiddleware(component string, next http.Handler) http.Handler {
	tracer := otel.Tracer("airush/" + component)
	prop := propagation.TraceContext{}
	m := &httpMetrics{
		requests: Counter(fmt.Sprintf("airush_%s_http_requests_total", component),
			"route", "method", "status"),
		duration: Histogram(fmt.Sprintf("airush_%s_http_request_duration_ms", component),
			"route", "method"),
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, component+".http "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", r.URL.Path),
			),
		)
		defer span.End()

		traceID := span.SpanContext().TraceID().String()
		// request 级 logger：覆盖必带字段 trace_id（tenant_id 由认证中间件在
		// spec-1.1 落地后覆盖，当前保持 "-"）。
		reqLogger := slog.Default().With("trace_id", traceID)
		ctx = ContextWithLogger(ctx, reqLogger)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(ctx))

		elapsed := float64(time.Since(start).Microseconds()) / 1000.0
		statusClass := fmt.Sprintf("%dxx", sw.status/100)
		m.requests.Add(ctx, 1, metric.WithAttributes(
			Labels("route", r.URL.Path, "method", r.Method, "status", statusClass)...))
		m.duration.Record(ctx, elapsed, metric.WithAttributes(
			Labels("route", r.URL.Path, "method", r.Method)...))

		reqLogger.Info("http request",
			"method", r.Method, "route", r.URL.Path,
			"status", sw.status, "duration_ms", elapsed)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap 暴露被包裹的 ResponseWriter，让 http.NewResponseController（Flush/Hijack/SetWriteDeadline）
// 与 httputil.ReverseProxy 的流式刷写穿透本中间件（SSE 事件流依赖，spec-1.8）。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush 直接实现 http.Flusher：老式 `w.(http.Flusher)` 断言的调用方也能刷。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
