package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

const testTenant = "00000000-0000-0000-0000-000000000001"

// fakeGate / fakeRecorder：进程内替身，记录被调用的参数。
type fakeGate struct {
	err   error
	calls int
}

func (g *fakeGate) Check(_ context.Context, _ string) error { g.calls++; return g.err }

type recorded struct {
	tenant, status, idem string
	usage                Usage
}

type fakeRecorder struct {
	mu   sync.Mutex
	got  []recorded
	fail error
}

func (r *fakeRecorder) Record(_ context.Context, tenantID string, u Usage, status, idem string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, recorded{tenantID, status, idem, u})
	return r.fail
}

func (r *fakeRecorder) last(t *testing.T) recorded {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		t.Fatal("recorder 未收到任何记账")
	}
	return r.got[len(r.got)-1]
}

// gatewayStub 是假网关：按请求体的 model/stream 决定回什么，并把收到的请求交给测试检视。
type gatewayStub struct {
	t        *testing.T
	lastReq  *http.Request
	lastBody []byte
	// 可注入的响应
	status  int
	body    string
	headers map[string]string
}

func (g *gatewayStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.lastReq = r.Clone(context.Background())
		g.lastBody, _ = io.ReadAll(r.Body)
		for k, v := range g.headers {
			w.Header().Set(k, v)
		}
		if g.status != 0 {
			w.WriteHeader(g.status)
		}
		_, _ = w.Write([]byte(g.body))
	})
}

func newStack(t *testing.T, stub *gatewayStub, gate QuotaGate, rec Recorder) (*httptest.Server, *http.Client, *Meter) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	m := NewMeter(http.DefaultTransport, gate, rec, WithMasterKey("mk-test"))
	return srv, &http.Client{Transport: m}, m
}

func post(t *testing.T, ctx context.Context, c *http.Client, url, body string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

func tenantCtx() context.Context {
	return WithCallInfo(tenancy.WithTenant(context.Background(), testTenant),
		CallInfo{AgentID: "agent-1", SessionID: "sess-1", TraceID: "tr-abc", Purpose: "chat"})
}

const chatJSON = `{"id":"x","object":"chat.completion","model":"deepseek-chat",
	"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`

// T1：chat 非流式 → Usage 三个数正确，正文原样到调用方
func TestChatNonStreamUsage(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatJSON, headers: map[string]string{
		"Content-Type": "application/json", "x-litellm-response-cost": "0.00123",
	}}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{}, rec)

	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","messages":[]}`)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"content":"hi"`) {
		t.Fatalf("正文未原样透传: %s", body)
	}
	got := rec.last(t)
	if got.tenant != testTenant || got.status != StatusOK {
		t.Fatalf("recorded = %+v", got)
	}
	u := got.usage
	if u.Model != "chat-default" || u.UpstreamModel != "deepseek-chat" ||
		u.PromptTokens != 11 || u.CompletionTokens != 3 || u.TotalTokens != 14 || u.Stream {
		t.Fatalf("usage = %+v", u)
	}
	if u.CostRefMicro == nil || *u.CostRefMicro != 1230 {
		t.Fatalf("cost_ref_micro = %v, want 1230", u.CostRefMicro)
	}
	if !strings.HasPrefix(got.idem, "tr-abc-") {
		t.Fatalf("idem key = %q, want trace 前缀", got.idem)
	}
}

const chatSSE = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-chat\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"deepseek-chat\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"total_tokens\":14}}\n\n" +
	"data: [DONE]\n\n"

// T2：chat 流式 → Meter 自动补 include_usage；末帧 usage 被提取；正文逐字节透传
func TestChatStreamAutoIncludeUsage(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatSSE, headers: map[string]string{"Content-Type": "text/event-stream"}}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{}, rec)

	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","stream":true,"messages":[]}`)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != chatSSE {
		t.Fatalf("流式正文被改写:\n%s", body)
	}
	// 请求体被补了 stream_options.include_usage
	var sent map[string]any
	_ = json.Unmarshal(stub.lastBody, &sent)
	so, _ := sent["stream_options"].(map[string]any)
	if so["include_usage"] != true {
		t.Fatalf("stream_options 未补: %s", stub.lastBody)
	}
	got := rec.last(t)
	if got.status != StatusOK || !got.usage.Stream || got.usage.TotalTokens != 14 || got.usage.UpstreamModel != "deepseek-chat" {
		t.Fatalf("recorded = %+v", got)
	}
}

// T2b：调用方自己声明了 stream_options → 原样尊重，不覆盖
func TestStreamOptionsRespected(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatSSE, headers: map[string]string{"Content-Type": "text/event-stream"}}
	srv, c, _ := newStack(t, stub, &fakeGate{}, &fakeRecorder{})
	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"m","stream":true,"stream_options":{"include_usage":false},"messages":[]}`)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(stub.lastBody), `"include_usage":false`) {
		t.Fatalf("调用方的 stream_options 被覆盖: %s", stub.lastBody)
	}
}

const responsesJSON = `{"id":"resp_x","object":"response","model":"deepseek-chat","output":[],
	"usage":{"input_tokens":21,"output_tokens":4,"total_tokens":25}}`

// T3：responses 非流式 → input/output 映射
func TestResponsesNonStreamUsage(t *testing.T) {
	stub := &gatewayStub{t: t, body: responsesJSON, headers: map[string]string{"Content-Type": "application/json"}}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{}, rec)
	req, _ := http.NewRequestWithContext(tenantCtx(), http.MethodPost, srv.URL+"/v1/responses",
		strings.NewReader(`{"model":"chat-default","input":"hi"}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	u := rec.last(t).usage
	if u.PromptTokens != 21 || u.CompletionTokens != 4 || u.TotalTokens != 25 || u.UpstreamModel != "deepseek-chat" {
		t.Fatalf("usage = %+v", u)
	}
}

const responsesSSE = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"deepseek-chat\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"deepseek-chat\",\"usage\":{\"input_tokens\":21,\"output_tokens\":4,\"total_tokens\":25}}}\n\n"

// T4：responses 流式 → response.completed 事件提取
func TestResponsesStreamUsage(t *testing.T) {
	stub := &gatewayStub{t: t, body: responsesSSE, headers: map[string]string{"Content-Type": "text/event-stream"}}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{}, rec)
	req, _ := http.NewRequestWithContext(tenantCtx(), http.MethodPost, srv.URL+"/v1/responses",
		strings.NewReader(`{"model":"chat-default","input":"hi","stream":true}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	got := rec.last(t)
	if got.status != StatusOK || got.usage.TotalTokens != 25 || !got.usage.Stream {
		t.Fatalf("recorded = %+v", got)
	}
}

// T5：流式中途断开（调用方读一半就 Close）→ aborted、tokens=0、仍计一次调用
func TestStreamAbortedRecordsZeroTokens(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatSSE, headers: map[string]string{"Content-Type": "text/event-stream"}}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{}, rec)
	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","stream":true,"messages":[]}`)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	buf := make([]byte, 16)
	_, _ = resp.Body.Read(buf) // 只读 16 字节，末帧远未到
	_ = resp.Body.Close()
	got := rec.last(t)
	if got.status != StatusAborted || got.usage.TotalTokens != 0 || !got.usage.Stream {
		t.Fatalf("recorded = %+v, want aborted/0 tokens/stream", got)
	}
}

// T6：配额门超额 → 请求不发往网关，返回 AR_QUOTA_EXCEEDED，记 quota_rejected
func TestQuotaExceededShortCircuits(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatJSON}
	rec := &fakeRecorder{}
	srv, c, _ := newStack(t, stub, &fakeGate{err: apierror.New(apierror.CodeQuotaExceeded)}, rec)
	_, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","messages":[]}`)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeQuotaExceeded {
		t.Fatalf("err = %v, want AR_QUOTA_EXCEEDED", err)
	}
	if stub.lastReq != nil {
		t.Fatal("超额时请求仍打到了网关")
	}
	if got := rec.last(t); got.status != StatusQuotaRejected || got.usage.Model != "chat-default" {
		t.Fatalf("recorded = %+v", got)
	}
}

// T7：配额门网络错误 → 放行 + 计数（fail-open）
func TestQuotaGateUnavailableFailsOpen(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatJSON, headers: map[string]string{"Content-Type": "application/json"}}
	rec := &fakeRecorder{}
	srv, c, m := newStack(t, stub, &fakeGate{err: errors.New("console unreachable")}, rec)
	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","messages":[]}`)
	if err != nil {
		t.Fatalf("应放行，实际: %v", err)
	}
	_ = resp.Body.Close()
	if m.QuotaCheckFailures.Load() != 1 {
		t.Fatalf("QuotaCheckFailures = %d, want 1", m.QuotaCheckFailures.Load())
	}
	if rec.last(t).status != StatusOK {
		t.Fatalf("recorded = %+v", rec.last(t))
	}
}

// T8：头注入齐全、Authorization 由 Meter 加、非流式 body 不被改写；无租户 ctx → 不出网
func TestHeadersInjectedAndBodyUntouched(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatJSON, headers: map[string]string{"Content-Type": "application/json"}}
	srv, c, _ := newStack(t, stub, &fakeGate{}, &fakeRecorder{})
	body := `{"model":"chat-default","messages":[{"role":"user","content":"x"}]}`
	resp, err := post(t, tenantCtx(), c, srv.URL, body)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
	h := stub.lastReq.Header
	if h.Get(HeaderTenant) != testTenant || h.Get(HeaderAgent) != "agent-1" ||
		h.Get(HeaderSession) != "sess-1" || h.Get(HeaderTrace) != "tr-abc" {
		t.Fatalf("headers = %v", h)
	}
	if h.Get("Authorization") != "Bearer mk-test" {
		t.Fatalf("Authorization = %q", h.Get("Authorization"))
	}
	if string(stub.lastBody) != body {
		t.Fatalf("非流式 body 被改写: %s", stub.lastBody)
	}

	// 无租户 ctx：不出网，AR_TENANT_CONTEXT_MISSING
	stub.lastReq = nil
	_, err = post(t, context.Background(), c, srv.URL, body)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTenantContextMissing || stub.lastReq != nil {
		t.Fatalf("无租户应 fail-closed: err=%v sent=%v", err, stub.lastReq != nil)
	}
}

// T9：错误映射——Invalid model name → MODEL_UNKNOWN；5xx → UPSTREAM_LLM_FAILED；
// 504 → TIMEOUT；连接拒绝 → UPSTREAM_LLM_FAILED；且上游正文不出现在返回错误里
func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   apierror.Code
	}{
		{"unknown model", 400, `{"error":{"message":"Invalid model name passed in model=nope","code":"400"}}`, apierror.CodeUpstreamLlmModelUnknown},
		{"upstream 500", 500, `{"error":{"message":"SECRET-UPSTREAM-DETAIL api_base=http://x"}}`, apierror.CodeUpstreamLlmFailed},
		{"gateway 504", 504, `timeout`, apierror.CodeUpstreamLlmTimeout},
		{"429", 429, `rate limited`, apierror.CodeUpstreamLlmFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := &gatewayStub{t: t, status: c.status, body: c.body}
			rec := &fakeRecorder{}
			srv, cl, _ := newStack(t, stub, &fakeGate{}, rec)
			_, err := post(t, tenantCtx(), cl, srv.URL, `{"model":"chat-default","messages":[]}`)
			var ae *apierror.Error
			if !errors.As(err, &ae) || ae.Code != c.want {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			if strings.Contains(err.Error(), "SECRET-UPSTREAM-DETAIL") {
				t.Fatalf("上游正文泄漏进返回错误: %v", err)
			}
			if rec.last(t).status != StatusUpstreamError {
				t.Fatalf("recorded = %+v", rec.last(t))
			}
		})
	}
	// 连接拒绝
	rec := &fakeRecorder{}
	m := NewMeter(http.DefaultTransport, &fakeGate{}, rec)
	cl := &http.Client{Transport: m}
	_, err := post(t, tenantCtx(), cl, "http://127.0.0.1:1", `{"model":"chat-default","messages":[]}`)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeUpstreamLlmFailed {
		t.Fatalf("connection refused → %v, want AR_UPSTREAM_LLM_FAILED", err)
	}
}

// T10：Recorder 失败不影响调用结果（错误只进日志）
func TestRecorderFailureDoesNotBreakCall(t *testing.T) {
	stub := &gatewayStub{t: t, body: chatJSON, headers: map[string]string{"Content-Type": "application/json"}}
	srv, c, _ := newStack(t, stub, &fakeGate{}, &fakeRecorder{fail: fmt.Errorf("db down")})
	resp, err := post(t, tenantCtx(), c, srv.URL, `{"model":"chat-default","messages":[]}`)
	if err != nil {
		t.Fatalf("记账失败不该影响调用: %v", err)
	}
	_ = resp.Body.Close()
}

// NewMeter 拒绝 nil 门/记账器：没有这两样就等于放弃防线，构造期就炸
func TestNewMeterRejectsNilDeps(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewMeter(nil gate) 应 panic")
		}
	}()
	NewMeter(nil, nil, &fakeRecorder{})
}
