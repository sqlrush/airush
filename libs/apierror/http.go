package apierror

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
)

// TraceHeader 是 trace_id 传入头；正式 trace 传播由 spec-0.9 中间件接管，
// 本包在无上游 trace 时自造 ID 保证 trace_id 必达（spec-0.8 §2.2）。
const TraceHeader = "X-Trace-Id"

// Response 是 API 错误响应体（spec-0.8 §2.2 定版）。
type Response struct {
	Code    Code     `json:"code"`
	Message string   `json:"message"`
	TraceID string   `json:"trace_id"`
	Details []Detail `json:"details,omitempty"`
}

// Handler 是可返回 error 的 HTTP handler；错误统一经 WriteError 出口。
type Handler func(w http.ResponseWriter, r *http.Request) error

// Middleware 包装 Handler：错误转标准响应、panic 恢复为 AR_INTERNAL
// （栈只进日志，响应体不含内部细节，spec-0.8 §3/Q4）。
func Middleware(next Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := traceIDFrom(r)
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "trace_id", traceID, "panic", rec)
				writeResponse(w, traceID, New(CodeInternalError))
			}
		}()
		if err := next(w, r); err != nil {
			ae, registered := FromError(err)
			if !registered {
				// 无码错误：告警级日志（内部细节留日志链）
				slog.Error("unregistered error reached middleware",
					"trace_id", traceID, "err", err)
			}
			writeResponse(w, traceID, ae)
		}
	})
}

// WriteError 直接写标准错误响应（非 Middleware 场景用）。
func WriteError(w http.ResponseWriter, traceID string, err error) {
	ae, _ := FromError(err)
	writeResponse(w, traceID, ae)
}

func writeResponse(w http.ResponseWriter, traceID string, ae *Error) {
	meta := MetaOf(ae.Code)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(meta.HTTP)
	_ = json.NewEncoder(w).Encode(Response{
		Code:    ae.Code,
		Message: meta.Message,
		TraceID: traceID,
		Details: ae.Details,
	})
}

// TraceIDFrom 取上游 trace_id，无则自造——供 Middleware 之外的错误出口
// （如认证中间件在进入 Handler 前就要拒绝）复用，保证 spec-0.8 §2.2 的
// "trace_id 必达"在每条错误路径上都成立，而不是只在 Middleware 覆盖的那条上。
func TraceIDFrom(r *http.Request) string { return traceIDFrom(r) }

func traceIDFrom(r *http.Request) string {
	if id := r.Header.Get(TraceHeader); id != "" {
		return id
	}
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return "tr_" + hex.EncodeToString(buf)
}
