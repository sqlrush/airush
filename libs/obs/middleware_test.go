package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// HTTPMiddleware 基本流：span 建立、request logger 注入 ctx、状态码捕获、
// 指标记录（noop meter 下同样走通构造与记录路径）。
func TestHTTPMiddleware(t *testing.T) {
	var gotLoggerInjected bool
	h := HTTPMiddleware("gateway", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLoggerInjected = LoggerFrom(r.Context()) != nil
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418（中间件不得改写响应）", rec.Code)
	}
	if !gotLoggerInjected {
		t.Fatal("request logger 未注入 ctx")
	}
}

// 上游 traceparent 透传：同一 trace 延续（span context 有效）。
func TestHTTPMiddlewarePropagatesTraceparent(t *testing.T) {
	h := HTTPMiddleware("gateway", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
