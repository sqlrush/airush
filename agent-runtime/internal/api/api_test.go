package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/agent-runtime/internal/runtime"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

const (
	tenantA = "00000000-0000-4000-8000-0000000000a1"
	threadX = "00000000-0000-7000-8000-0000000000e1"
)

// fakeCore 记录调用并按需回错。
type fakeCore struct {
	startErr, turnErr, intErr, resumeErr, evErr error
	turn                                        runtime.TurnRef
	events                                      []runtime.Event
	seenTenant                                  string
	seenInput                                   runtime.TurnInput
	seenStart                                   runtime.StartThreadInput
	seenFrom                                    int64
}

func (f *fakeCore) StartThread(ctx context.Context, in runtime.StartThreadInput) (runtime.ThreadRef, error) {
	f.seenTenant, _ = tenancy.FromContext(ctx)
	f.seenStart = in
	return runtime.ThreadRef{ThreadID: threadX}, f.startErr
}

func (f *fakeCore) SubmitTurn(_ context.Context, _ string, in runtime.TurnInput) (runtime.TurnRef, error) {
	f.seenInput = in
	return f.turn, f.turnErr
}
func (f *fakeCore) Interrupt(context.Context, string) error    { return f.intErr }
func (f *fakeCore) ResumeThread(context.Context, string) error { return f.resumeErr }
func (f *fakeCore) Events(ctx context.Context, _ string, from int64) (<-chan runtime.Event, error) {
	if f.evErr != nil {
		return nil, f.evErr
	}
	f.seenFrom = from
	ch := make(chan runtime.Event, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func newTestServer(t *testing.T, core *fakeCore) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(core, nil, "tok").Handler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set(HeaderTenant, tenantA)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAuthAndTenantGates(t *testing.T) {
	srv := newTestServer(t, &fakeCore{})
	if code, b := call(t, srv, "POST", "/internal/v1/agent/threads", `{}`, map[string]string{"Authorization": "Bearer nope"}); code != 401 || !strings.Contains(string(b), "AR_SVC_UNAUTHENTICATED") {
		t.Fatalf("bad token = %d %s", code, b)
	}
	if code, b := call(t, srv, "POST", "/internal/v1/agent/threads", `{}`, map[string]string{HeaderTenant: "not-a-uuid"}); code != 500 || !strings.Contains(string(b), "AR_TENANT_CONTEXT_MISSING") {
		t.Fatalf("bad tenant = %d %s", code, b)
	}
}

func TestCreateThread(t *testing.T) {
	core := &fakeCore{}
	srv := newTestServer(t, core)
	code, b := call(t, srv, "POST", "/internal/v1/agent/threads", `{"tenant_id":"`+tenantA+`","agent_id":"`+threadX+`","model":"m","title":"t"}`, nil)
	if code != 201 || !strings.Contains(string(b), threadX) {
		t.Fatalf("create = %d %s", code, b)
	}
	if core.seenTenant != tenantA || core.seenStart.AgentID != threadX || core.seenStart.Model != "m" || core.seenStart.Title != "t" {
		t.Fatalf("core saw %+v tenant %s", core.seenStart, core.seenTenant)
	}
	cases := map[string]string{
		"tenant mismatch": `{"tenant_id":"11111111-1111-4111-8111-111111111111"}`,
		"bad agent id":    `{"agent_id":"x"}`,
		"unknown field":   `{"nope":1}`,
		"long title":      `{"title":"` + strings.Repeat("x", 201) + `"}`,
	}
	for name, body := range cases {
		if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads", body, nil); code != 400 {
			t.Fatalf("%s: code = %d", name, code)
		}
	}
	// 空 body 合法（全部缺省）
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads", ``, nil); code != 201 {
		t.Fatalf("empty body = %d", code)
	}
	core.startErr = threadstore.NewThreadNotFoundError(protocol.NewThreadID(threadX))
	if code, b := call(t, srv, "POST", "/internal/v1/agent/threads", `{}`, nil); code != 404 || !strings.Contains(string(b), "AR_AGENT_THREAD_NOT_FOUND") {
		t.Fatalf("mapped store error = %d %s", code, b)
	}
}

func TestSubmitTurnInterruptResume(t *testing.T) {
	core := &fakeCore{turn: runtime.TurnRef{TurnID: "t1"}}
	srv := newTestServer(t, core)
	code, b := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/turns", `{"input":[{"type":"text","text":"hi"}]}`, nil)
	if code != 200 || !strings.Contains(string(b), `"turn_id":"t1"`) {
		t.Fatalf("turn = %d %s", code, b)
	}
	if len(core.seenInput.Items) != 1 || core.seenInput.Items[0].Text != "hi" {
		t.Fatalf("input = %+v", core.seenInput)
	}
	core.turn = runtime.TurnRef{Queued: true}
	if code, b := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/turns", `{"input":[{"type":"text","text":"hi"}]}`, nil); code != 202 || !strings.Contains(string(b), `"queued":true`) {
		t.Fatalf("queued = %d %s", code, b)
	}
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads/not-uuid/turns", `{"input":[{"type":"text","text":"hi"}]}`, nil); code != 400 {
		t.Fatalf("bad id = %d", code)
	}
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/turns", `{"input":[]}`, nil); code != 400 {
		t.Fatalf("empty input = %d", code)
	}
	core.turnErr = apierror.New(apierror.CodeAgentTurnRejected)
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/turns", `{"input":[{"type":"text","text":"hi"}]}`, nil); code != 429 {
		t.Fatalf("rejected = %d", code)
	}
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/interrupt", ``, nil); code != 204 {
		t.Fatalf("interrupt = %d", code)
	}
	if code, _ := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/resume", ``, nil); code != 204 {
		t.Fatalf("resume = %d", code)
	}
	core.resumeErr = errors.New("boom")
	if code, b := call(t, srv, "POST", "/internal/v1/agent/threads/"+threadX+"/resume", ``, nil); code != 500 || !strings.Contains(string(b), "AR_INTERNAL_ERROR") {
		t.Fatalf("resume err = %d %s", code, b)
	}
	// 只读端点没有 engine → 500（纯 core 装配不提供列表）
	if code, _ := call(t, srv, "GET", "/internal/v1/agent/threads", ``, nil); code != 500 {
		t.Fatalf("list without engine = %d", code)
	}
}

func TestEventsSSE(t *testing.T) {
	core := &fakeCore{events: []runtime.Event{
		{Seq: 3, Type: "task_started", TurnID: "t1", Payload: json.RawMessage(`{"a":1}`)},
		{Seq: 4, Type: "agent_message", Payload: json.RawMessage(`{"b":2}`), PayloadRef: "thread/x/seq/4"},
	}}
	srv := newTestServer(t, core)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/v1/agent/threads/"+threadX+"/events", nil)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set(HeaderTenant, tenantA)
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse status/ct = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if core.seenFrom != 3 {
		t.Fatalf("Last-Event-ID 2 → from_seq 3, got %d", core.seenFrom)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"id: 3", "event: task_started", `"turn_id":"t1"`, "id: 4", "event: agent_message", `"payload_ref":"thread/x/seq/4"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sse missing %q in:\n%s", want, joined)
		}
	}
	// from_seq 参数与错误路径
	if code, _ := call(t, srv, "GET", "/internal/v1/agent/threads/"+threadX+"/events?from_seq=-1", ``, nil); code != 400 {
		t.Fatalf("bad from_seq = %d", code)
	}
	core.evErr = apierror.New(apierror.CodeAgentThreadNotFound)
	if code, _ := call(t, srv, "GET", "/internal/v1/agent/threads/"+threadX+"/events", ``, nil); code != 404 {
		t.Fatalf("events not found = %d", code)
	}
}

func TestMapErr(t *testing.T) {
	if mapErr(nil) != nil {
		t.Fatal("nil")
	}
	ae, _ := apierror.FromError(mapErr(threadstore.NewUnsupportedError("x")))
	if ae.Code != apierror.CodeValidationFailed {
		t.Fatalf("unsupported → %s", ae.Code)
	}
	ae, _ = apierror.FromError(mapErr(threadstore.NewInternalError(errors.New("db"), "x")))
	if ae.Code != apierror.CodeInternalError {
		t.Fatalf("internal → %s", ae.Code)
	}
	orig := apierror.New(apierror.CodeQuotaExceeded)
	if !errors.Is(mapErr(orig), orig) {
		t.Fatal("apierror passthrough")
	}
}
