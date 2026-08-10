package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEveryRegisteredCode spec-0.8 T1：每个注册码触发一次，断言 code/http/level
// 与响应形态（规则 4：每个错误码有触发用例）。
func TestEveryRegisteredCode(t *testing.T) {
	t.Parallel()
	if len(codeMeta) < 15 {
		t.Fatalf("expected >=15 codes, got %d", len(codeMeta))
	}
	for code, meta := range codeMeta {
		t.Run(string(code), func(t *testing.T) {
			t.Parallel()
			if meta.Message == "" || meta.HTTP < 400 || meta.HTTP > 599 {
				t.Fatalf("bad meta: %+v", meta)
			}
			if !strings.HasPrefix(meta.Level, "E") {
				t.Fatalf("bad level: %s", meta.Level)
			}
			rec := httptest.NewRecorder()
			WriteError(rec, "tr_test", New(code))
			if rec.Code != meta.HTTP {
				t.Fatalf("http = %d, want %d", rec.Code, meta.HTTP)
			}
			var resp Response
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Code != code || resp.Message != meta.Message || resp.TraceID != "tr_test" {
				t.Fatalf("resp = %+v", resp)
			}
		})
	}
}

// T2：未注册裸 error 经中间件 → AR_INTERNAL + 500，响应不含内部错误文本。
func TestUnregisteredErrorBecomesInternal(t *testing.T) {
	t.Parallel()
	h := Middleware(func(w http.ResponseWriter, r *http.Request) error {
		return fmt.Errorf("db at 10.0.0.5 exploded")
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != 500 {
		t.Fatalf("http = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "10.0.0.5") {
		t.Fatalf("internal detail leaked: %s", body)
	}
	if !strings.Contains(body, string(CodeInternalError)) {
		t.Fatalf("expected AR_INTERNAL, got %s", body)
	}
}

// T3：panic → 500 标准响应，body 无栈。
func TestPanicRecovered(t *testing.T) {
	t.Parallel()
	h := Middleware(func(w http.ResponseWriter, r *http.Request) error {
		panic("boom with secret path /etc/x")
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rec.Code != 500 {
		t.Fatalf("http = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic detail leaked: %s", rec.Body.String())
	}
}

// T7：errors.Is/As 穿透 wrap 链。
func TestWrapChain(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("row not found")
	err := fmt.Errorf("query tenant: %w", Wrap(CodeTenantNotFound, sentinel))

	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeTenantNotFound {
		t.Fatalf("errors.As failed: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is lost the sentinel")
	}
}

// details 通道（E1 验证类）。
func TestValidationDetails(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	WriteError(rec, "tr_x", New(CodeValidationFailed).
		WithDetails(Detail{Field: "port", Reason: "必须在 1-65535 之间"}))

	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Details) != 1 || resp.Details[0].Field != "port" {
		t.Fatalf("details = %+v", resp.Details)
	}
}

// trace 头透传。
func TestTraceHeaderPropagated(t *testing.T) {
	t.Parallel()
	h := Middleware(func(w http.ResponseWriter, r *http.Request) error {
		return New(CodeQuotaExceeded)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(TraceHeader, "tr_upstream")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.TraceID != "tr_upstream" {
		t.Fatalf("trace_id = %s, want tr_upstream", resp.TraceID)
	}
}
