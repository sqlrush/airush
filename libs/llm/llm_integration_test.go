//go:build integration

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/testkit"
	"github.com/sqlrush/airush/testkit/mockllm"
)

// 集成层：**真 LiteLLM 容器**（与 Helm 同 digest）+ 进程内 mock 供应商 + 我们的 Meter，
// 验的是 spec-1.7 T11-T16——那些只有真网关在场才有意义的行为（桥接、fallback、
// 错误形态、日志、metrics）。

type stack struct {
	mock   *mockllm.Handler
	lite   *testkit.LiteLLM
	rec    *fakeRecorder
	client *http.Client
	meter  *Meter
}

func startStack(t *testing.T) *stack {
	t.Helper()
	ctx := context.Background()
	mock := mockllm.New(nil)
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	lite, err := testkit.StartLiteLLM(ctx, port, []testkit.LiteLLMModel{
		{Name: "chat-default", Upstream: "deepseek/mock-default"}, // 原生前缀 → 桥接
		{Name: "chat-openai", Upstream: "openai/mock-openai"},     // openai/ 前缀 → 透传
		{Name: "chat-fail", Upstream: "deepseek/mock-fail"},       // 上游恒 500
	}, map[string][]string{"chat-fail": {"chat-default"}})
	if err != nil {
		t.Fatalf("start litellm: %v", err)
	}
	t.Cleanup(func() { _ = lite.Terminate(context.Background()) })

	rec := &fakeRecorder{}
	m := NewMeter(http.DefaultTransport, &fakeGate{}, rec, WithMasterKey(lite.MasterKey))
	return &stack{mock: mock, lite: lite, rec: rec, client: &http.Client{Transport: m}, meter: m}
}

func (s *stack) do(t *testing.T, path, body string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(tenantCtx(), http.MethodPost, s.lite.BaseURL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return s.client.Do(req)
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(b)
}

// T11：经 LiteLLM 打 chat（非流式/流式）到 mock，Meter 拿到的 usage 与 mock 发出的一致
func TestThroughLiteLLMChatUsage(t *testing.T) {
	s := startStack(t)

	resp, err := s.do(t, "/v1/chat/completions", `{"model":"chat-default","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatalf("non-stream: %v", err)
	}
	if body := readAll(t, resp); !strings.Contains(body, mockllm.ReplyText) {
		t.Fatalf("body = %s", body)
	}
	got := s.rec.last(t)
	if got.status != StatusOK || got.usage.TotalTokens != mockllm.TotalTokens || got.usage.Model != "chat-default" {
		t.Fatalf("non-stream recorded = %+v", got)
	}

	resp, err = s.do(t, "/v1/chat/completions", `{"model":"chat-default","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if body := readAll(t, resp); !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream body = %s", body)
	}
	got = s.rec.last(t)
	if got.status != StatusOK || !got.usage.Stream || got.usage.TotalTokens != mockllm.TotalTokens {
		t.Fatalf("stream recorded = %+v", got)
	}
}

// T12：Responses API——原生前缀桥接成功（含工具调用回合）；openai/ 前缀透传 → 上游 404 → UPSTREAM_LLM_FAILED
func TestThroughLiteLLMResponsesBridge(t *testing.T) {
	s := startStack(t)

	// 桥接：纯文本
	resp, err := s.do(t, "/v1/responses", `{"model":"chat-default","input":"hi"}`)
	if err != nil {
		t.Fatalf("responses via deepseek/ prefix: %v", err)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, mockllm.ReplyText) {
		t.Fatalf("bridge body = %.300s", body)
	}
	if got := s.rec.last(t); got.usage.TotalTokens != mockllm.TotalTokens {
		t.Fatalf("bridge usage = %+v（Responses 形态的 input/output_tokens 未被提取）", got.usage)
	}

	// 桥接：带工具定义 → 上游 tool_call 应被翻译成 Responses 的 function_call 输出
	resp, err = s.do(t, "/v1/responses", `{"model":"chat-default","input":"conn?",
		"tools":[{"type":"function","name":"lookup_metric","parameters":{"type":"object","properties":{"name":{"type":"string"}}}}]}`)
	if err != nil {
		t.Fatalf("responses with tools: %v", err)
	}
	body = readAll(t, resp)
	if !strings.Contains(body, "function_call") || !strings.Contains(body, mockllm.ToolName) {
		t.Fatalf("工具调用未经桥接翻译成 function_call: %.400s", body)
	}

	// 透传：openai/ 前缀 → mock 没有 /v1/responses → 404 → 我们映射为 UPSTREAM_LLM_FAILED
	_, err = s.do(t, "/v1/responses", `{"model":"chat-openai","input":"hi"}`)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeUpstreamLlmFailed {
		t.Fatalf("openai/ 前缀应透传失败并映射为 UPSTREAM_LLM_FAILED，实际: %v", err)
	}
}

// T13：主模型上游 5xx → fallback 到备用成功；upstream_model 记为备用
func TestThroughLiteLLMFallback(t *testing.T) {
	s := startStack(t)
	resp, err := s.do(t, "/v1/chat/completions", `{"model":"chat-fail","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatalf("fallback 未生效: %v", err)
	}
	readAll(t, resp)
	got := s.rec.last(t)
	if got.status != StatusOK || got.usage.Model != "chat-fail" {
		t.Fatalf("recorded = %+v", got)
	}
	if got.usage.UpstreamModel == "" || strings.Contains(got.usage.UpstreamModel, "fail") {
		t.Fatalf("upstream_model = %q, want 备用模型（非 fail）", got.usage.UpstreamModel)
	}
}

// T14：未知模型 → AR_UPSTREAM_LLM_MODEL_UNKNOWN
func TestThroughLiteLLMUnknownModel(t *testing.T) {
	s := startStack(t)
	_, err := s.do(t, "/v1/chat/completions", `{"model":"nope","messages":[{"role":"user","content":"hi"}]}`)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeUpstreamLlmModelUnknown {
		t.Fatalf("err = %v, want AR_UPSTREAM_LLM_MODEL_UNKNOWN", err)
	}
}

// T15：/metrics 带 key 200 且含 token 计数；无 key 401
func TestThroughLiteLLMMetrics(t *testing.T) {
	s := startStack(t)
	// 先打一笔让计数器有值
	resp, err := s.do(t, "/v1/chat/completions", `{"model":"chat-default","messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	readAll(t, resp)

	get := func(auth bool) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, s.lite.BaseURL+"/metrics/", nil)
		if auth {
			req.Header.Set("Authorization", "Bearer "+s.lite.MasterKey)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /metrics: %v", err)
		}
		return r.StatusCode, readAll(t, r)
	}
	if code, _ := get(false); code != http.StatusUnauthorized {
		t.Fatalf("/metrics 无 key 应 401，实际 %d", code)
	}
	code, body := get(true)
	if code != http.StatusOK || !strings.Contains(body, "litellm_total_tokens_metric_total") {
		t.Fatalf("/metrics 带 key: %d, 含 token 计数=%v", code, strings.Contains(body, "litellm_total_tokens"))
	}
}

// T16：LiteLLM 容器 json 日志在成功路径不含 prompt canary（AD-3）
func TestThroughLiteLLMLogsNoPrompt(t *testing.T) {
	s := startStack(t)
	const canary = "CANARY-PROMPT-7f3a9c"
	resp, err := s.do(t, "/v1/chat/completions", `{"model":"chat-default","messages":[{"role":"user","content":"`+canary+`"}]}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	readAll(t, resp)
	logs, err := s.lite.Logs(context.Background())
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if strings.Contains(logs, canary) {
		t.Fatal("LiteLLM 日志包含 prompt 内容——AD-3 违规（json_logs 成功路径不该记 content）")
	}
	// 顺带确认日志确实是 json 形态（不是没开日志所以才"没有"）
	var probe map[string]any
	line := firstJSONLine(logs)
	if line == "" || json.Unmarshal([]byte(line), &probe) != nil {
		t.Fatalf("LiteLLM 日志不是 json 行形态，无法据此断言:\n%.500s", logs)
	}
}

func firstJSONLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "{") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}
