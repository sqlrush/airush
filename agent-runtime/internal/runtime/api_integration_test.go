//go:build integration

package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqlrush/airush/agent-runtime/internal/api"
	rt "github.com/sqlrush/airush/agent-runtime/internal/runtime"
)

// 本文件在 runtime_test 包里复用 runtime 包的集成夹具（导出的测试钩子见 export_integration_test.go）。

// TestInternalAPIEndToEnd spec-1.8 T8/T10：经内部 HTTP 面走完 建线程 → 发一轮 → SSE 回放+实时 →
// 列表/历史 → 删除；svc token 与租户头是硬门槛。
func TestInternalAPIEndToEnd(t *testing.T) {
	ctx, tenantID := rt.NewTestTenant(t)
	llmSrv := rt.NewTestLLM(t)
	e := rt.NewTestEngine(t, llmSrv, "pod-api")
	srv := httptest.NewServer(api.New(e, e, "svc-secret").Handler())
	t.Cleanup(srv.Close)
	_ = ctx

	do := func(method, path, body string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer svc-secret")
		req.Header.Set(api.HeaderTenant, tenantID)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, b
	}

	// 认证 / 租户门
	if resp, _ := do("POST", "/internal/v1/agent/threads", `{}`, map[string]string{"Authorization": "Bearer wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d", resp.StatusCode)
	}
	// 缺租户头 = 调用方（console 反代）缺陷：按错误码注册表回 500 + AR_TENANT_CONTEXT_MISSING（fail-closed）。
	if resp, b := do("POST", "/internal/v1/agent/threads", `{}`, map[string]string{api.HeaderTenant: ""}); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("missing tenant status = %d body=%s", resp.StatusCode, b)
	} else if !bytes.Contains(b, []byte("AR_TENANT_CONTEXT_MISSING")) {
		t.Fatalf("missing tenant body = %s", b)
	}
	if resp, b := do("POST", "/internal/v1/agent/threads", fmt.Sprintf(`{"tenant_id":%q}`, "11111111-1111-1111-1111-111111111111"), nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched body tenant = %d %s", resp.StatusCode, b)
	}

	// 建线程
	resp, b := do("POST", "/internal/v1/agent/threads", `{"title":"api 线程"}`, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, b)
	}
	var created struct {
		ThreadID string `json:"thread_id"`
	}
	_ = json.Unmarshal(b, &created)
	if created.ThreadID == "" {
		t.Fatalf("no thread id: %s", b)
	}

	// 发一轮
	resp, b = do("POST", "/internal/v1/agent/threads/"+created.ThreadID+"/turns", `{"input":[{"type":"text","text":"你好"}]}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn = %d %s", resp.StatusCode, b)
	}
	var turn struct {
		TurnID string `json:"turn_id"`
		Queued bool   `json:"queued"`
	}
	_ = json.Unmarshal(b, &turn)
	if turn.TurnID == "" || turn.Queued {
		t.Fatalf("turn resp = %s", b)
	}
	if resp, b := do("POST", "/internal/v1/agent/threads/"+created.ThreadID+"/turns", `{"input":[]}`, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty input = %d %s", resp.StatusCode, b)
	}

	// SSE：从头回放，直到 task_complete
	sseCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(sseCtx, http.MethodGet, srv.URL+"/internal/v1/agent/threads/"+created.ThreadID+"/events?from_seq=1", nil)
	req.Header.Set("Authorization", "Bearer svc-secret")
	req.Header.Set(api.HeaderTenant, tenantID)
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer func() { _ = sseResp.Body.Close() }()
	if ct := sseResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse content-type = %s", ct)
	}
	var seen []string
	var lastID string
	scanner := bufio.NewScanner(sseResp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			lastID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			seen = append(seen, strings.TrimPrefix(line, "event: "))
		}
		if len(seen) > 0 && seen[len(seen)-1] == "task_complete" {
			break
		}
	}
	cancel()
	if !containsStr(seen, "session_meta") || !containsStr(seen, "task_started") || !containsStr(seen, "task_complete") {
		t.Fatalf("sse events = %v", seen)
	}
	if lastID == "" || lastID == "0" {
		t.Fatalf("sse id header missing: %q", lastID)
	}

	// 列表 / 详情 / 历史
	resp, b = do("GET", "/internal/v1/agent/threads?limit=10", "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(b, []byte(created.ThreadID)) {
		t.Fatalf("list = %d %s", resp.StatusCode, b)
	}
	resp, b = do("GET", "/internal/v1/agent/threads/"+created.ThreadID, "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(b, []byte(`"status":"idle"`)) {
		t.Fatalf("get = %d %s", resp.StatusCode, b)
	}
	resp, b = do("GET", "/internal/v1/agent/threads/"+created.ThreadID+"/items?limit=50", "", nil)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(b, []byte("mock reply")) {
		t.Fatalf("items = %d %s", resp.StatusCode, b)
	}
	// 另一租户看不到
	other := "22222222-2222-4222-8222-222222222222"
	if resp, _ := do("GET", "/internal/v1/agent/threads/"+created.ThreadID, "", map[string]string{api.HeaderTenant: other}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d", resp.StatusCode)
	}
	// 删除
	if resp, b := do("DELETE", "/internal/v1/agent/threads/"+created.ThreadID, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d %s", resp.StatusCode, b)
	}
	if resp, _ := do("GET", "/internal/v1/agent/threads/"+created.ThreadID, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete = %d", resp.StatusCode)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
