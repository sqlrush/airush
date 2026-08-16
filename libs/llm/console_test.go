package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqlrush/airush/libs/apierror"
)

// consoleStub 是假 console 内部 API：按路径与计数决定响应，记录收到的载荷。
type consoleStub struct {
	quotaStatus  int
	usageStatus  []int // 按调用次序返回；耗尽后重复最后一个
	usageCalls   atomic.Int64
	lastUsage    usageRequest
	lastAuth     string
	quotaPayload map[string]any
}

func (s *consoleStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/internal/v1/llm/quota-check", func(w http.ResponseWriter, r *http.Request) {
		s.lastAuth = r.Header.Get("Authorization")
		w.WriteHeader(s.quotaStatus)
		_ = json.NewEncoder(w).Encode(s.quotaPayload)
	})
	mux.HandleFunc("/internal/v1/llm/usage", func(w http.ResponseWriter, r *http.Request) {
		n := int(s.usageCalls.Add(1))
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &s.lastUsage)
		st := s.usageStatus[min(n-1, len(s.usageStatus)-1)]
		w.WriteHeader(st)
	})
	return mux
}

func newConsole(t *testing.T, stub *consoleStub) *ConsoleClient {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	c := NewConsoleClient(srv.URL, "svc-auth-for-test")
	c.sleep = func(time.Duration) {} // 重试不真等
	return c
}

func TestConsoleClientCheck(t *testing.T) {
	t.Run("200 有余额", func(t *testing.T) {
		stub := &consoleStub{quotaStatus: 200, quotaPayload: map[string]any{"budget": 100, "used": 1, "remaining_tokens": 99}}
		c := newConsole(t, stub)
		if err := c.Check(context.Background(), testTenant); err != nil {
			t.Fatalf("check: %v", err)
		}
		if stub.lastAuth != "Bearer svc-auth-for-test" {
			t.Fatalf("svc token 未带: %q", stub.lastAuth)
		}
	})
	t.Run("429 → AR_QUOTA_EXCEEDED", func(t *testing.T) {
		c := newConsole(t, &consoleStub{quotaStatus: 429, quotaPayload: map[string]any{"code": "AR_QUOTA_EXCEEDED"}})
		err := c.Check(context.Background(), testTenant)
		var ae *apierror.Error
		if !errors.As(err, &ae) || ae.Code != apierror.CodeQuotaExceeded {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("500 → 普通错误（Meter 据此 fail-open）", func(t *testing.T) {
		c := newConsole(t, &consoleStub{quotaStatus: 500, quotaPayload: map[string]any{}})
		err := c.Check(context.Background(), testTenant)
		var ae *apierror.Error
		if err == nil || errors.As(err, &ae) {
			t.Fatalf("500 应为非 apierror 的普通错误，实际 %v", err)
		}
	})
	t.Run("网络不可达 → 普通错误", func(t *testing.T) {
		c := NewConsoleClient("http://127.0.0.1:1", "x")
		if err := c.Check(context.Background(), testTenant); err == nil {
			t.Fatal("应报错")
		}
	})
}

func TestConsoleClientRecord(t *testing.T) {
	cost := int64(1230)
	u := Usage{Model: "chat-default", UpstreamModel: "deepseek-chat", PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14, CostRefMicro: &cost, Stream: true}
	ctx := WithCallInfo(context.Background(), CallInfo{AgentID: "a1", SessionID: "s1", TraceID: "tr1", Purpose: "chat"})

	t.Run("202 一次成功，载荷齐全", func(t *testing.T) {
		stub := &consoleStub{usageStatus: []int{202}}
		c := newConsole(t, stub)
		if err := c.Record(ctx, testTenant, u, StatusOK, "tr1-1"); err != nil {
			t.Fatalf("record: %v", err)
		}
		got := stub.lastUsage
		if got.TenantID != testTenant || got.IdemKey != "tr1-1" || got.Status != StatusOK ||
			got.Usage.Model != "chat-default" || got.Usage.TotalTokens != 14 || got.Usage.AgentID != "a1" ||
			got.Usage.TraceID != "tr1" || got.Usage.CostRefMicro == nil || *got.Usage.CostRefMicro != 1230 || !got.Usage.Stream {
			t.Fatalf("payload = %+v", got)
		}
	})
	t.Run("5xx 重试后成功", func(t *testing.T) {
		stub := &consoleStub{usageStatus: []int{500, 503, 202}}
		c := newConsole(t, stub)
		if err := c.Record(ctx, testTenant, u, StatusOK, "k"); err != nil {
			t.Fatalf("record: %v", err)
		}
		if stub.usageCalls.Load() != 3 {
			t.Fatalf("calls = %d, want 3", stub.usageCalls.Load())
		}
	})
	t.Run("持续 5xx → 重试耗尽报错", func(t *testing.T) {
		stub := &consoleStub{usageStatus: []int{500}}
		c := newConsole(t, stub)
		if err := c.Record(ctx, testTenant, u, StatusOK, "k"); err == nil {
			t.Fatal("应报错")
		}
		if stub.usageCalls.Load() != 4 { // 1 + 3 次重试
			t.Fatalf("calls = %d, want 4", stub.usageCalls.Load())
		}
	})
	t.Run("4xx 不重试", func(t *testing.T) {
		stub := &consoleStub{usageStatus: []int{400}}
		c := newConsole(t, stub)
		if err := c.Record(ctx, testTenant, u, StatusOK, "k"); err == nil {
			t.Fatal("应报错")
		}
		if stub.usageCalls.Load() != 1 {
			t.Fatalf("4xx 不该重试，calls = %d", stub.usageCalls.Load())
		}
	})
}

func TestParseCostHeader(t *testing.T) {
	if parseCostHeader("") != nil || parseCostHeader("abc") != nil || parseCostHeader("-1") != nil {
		t.Fatal("空/非法/负数应为 nil")
	}
	if v := parseCostHeader("0.001"); v == nil || *v != 1000 {
		t.Fatalf("0.001 USD → %v micro, want 1000", v)
	}
}
