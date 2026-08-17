package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAgentProxyRewrite spec-1.8 D4：/api/v1/agent/* → /internal/v1/agent/*，注入 svc token 与
// X-Airush-Tenant，剥掉客户端 Cookie；SSE 头原样透传；上游不可达 → AR_INTERNAL_ERROR。
func TestAgentProxyRewrite(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "id: 1\nevent: task_started\ndata: {}\n\n")
	}))
	defer upstream.Close()

	s := &Server{defaultTenantID: "00000000-0000-0000-0000-000000000001"}
	if _, err := s.WithAgentRuntime(upstream.URL, "svc-tok"); err != nil {
		t.Fatalf("with agent runtime: %v", err)
	}
	h := s.tenantMiddleware(s.agent)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/threads/abc/events?from_seq=2", nil)
	req.Header.Set("Cookie", "session=secret")
	req.Header.Set("Authorization", "Bearer client-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "event: task_started") {
		t.Fatalf("proxied response = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if got.URL.Path != "/internal/v1/agent/threads/abc/events" || got.URL.RawQuery != "from_seq=2" {
		t.Fatalf("upstream path = %s?%s", got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer svc-tok" || got.Header.Get("X-Airush-Tenant") != s.defaultTenantID || got.Header.Get("Cookie") != "" {
		t.Fatalf("upstream headers = %v", got.Header)
	}

	// 无 runtime URL：不挂
	plain := &Server{defaultTenantID: s.defaultTenantID}
	if _, err := plain.WithAgentRuntime("", ""); err != nil || plain.agent != nil {
		t.Fatalf("empty url must not mount: %v %v", err, plain.agent)
	}
	if _, err := plain.WithAgentRuntime("://bad", ""); err == nil {
		t.Fatal("bad url accepted")
	}

	// 上游挂了 → 标准错误体
	upstream.Close()
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/v1/agent/threads", strings.NewReader(`{}`)))
	if rec2.Code != http.StatusInternalServerError || !strings.Contains(rec2.Body.String(), "AR_INTERNAL_ERROR") {
		t.Fatalf("upstream down = %d %s", rec2.Code, rec2.Body.String())
	}
}
