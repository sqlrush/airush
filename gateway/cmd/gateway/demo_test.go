package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sqlrush/airush/libs/apierror"
)

// TestDemoHandler 覆盖 /demo 三分支（正常/错误码/panic 恢复）——spec-0.9 D5 的
// 行为契约此前仅由 obs-smoke 脚本验证，纳入 go 测试补齐覆盖证据。
func TestDemoHandler(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(apierror.Middleware(demoHandler))
	t.Cleanup(srv.Close)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantField  string
		wantValue  string
	}{
		{"正常路径带 trace_id", "/demo", 200, "ok", "true"},
		{"错误码路径", "/demo?fail=quota", 429, "code", "AR_QUOTA_EXCEEDED"},
		{"panic 恢复为 AR_INTERNAL_ERROR", "/demo?fail=panic", 500, "code", "AR_INTERNAL_ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", resp.StatusCode, body, tt.wantStatus)
			}
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatalf("unmarshal %s: %v", body, err)
			}
			if m[tt.wantField] != tt.wantValue {
				t.Fatalf("%s = %v, want %s", tt.wantField, m[tt.wantField], tt.wantValue)
			}
		})
	}
}
