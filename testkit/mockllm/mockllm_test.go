package mockllm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, srv *httptest.Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func TestChatNonStreamCarriesFixedUsage(t *testing.T) {
	h := New(nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp := post(t, srv, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Choices[0].Message.Content != ReplyText || out.Choices[0].FinishReason != "stop" {
		t.Fatalf("choice = %+v", out.Choices[0])
	}
	if out.Usage.PromptTokens != PromptTokens || out.Usage.TotalTokens != TotalTokens {
		t.Fatalf("usage = %+v", out.Usage)
	}
	if h.Requests.Load() != 1 {
		t.Fatalf("requests = %d", h.Requests.Load())
	}
}

func TestChatStreamEndsWithUsageAndDone(t *testing.T) {
	srv := httptest.NewServer(New(nil))
	defer srv.Close()

	resp := post(t, srv, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer func() { _ = resp.Body.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	body := sb.String()
	if !strings.Contains(body, `"usage":{"prompt_tokens":11`) || !strings.HasSuffix(strings.TrimSpace(body), "[DONE]") {
		t.Fatalf("stream body missing usage or DONE:\n%s", body)
	}
}

func TestToolRoundTrip(t *testing.T) {
	srv := httptest.NewServer(New(nil))
	defer srv.Close()

	// 第一回合：带 tools、最后一条 user → tool_call
	resp := post(t, srv, `{"model":"m","tools":[{"type":"function","function":{"name":"lookup_metric"}}],
		"messages":[{"role":"user","content":"conn?"}]}`)
	var first struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					Function struct{ Name string } `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&first)
	_ = resp.Body.Close()
	if first.Choices[0].FinishReason != "tool_calls" || first.Choices[0].Message.ToolCalls[0].Function.Name != ToolName {
		t.Fatalf("first turn = %+v", first.Choices[0])
	}

	// 第二回合：最后一条 tool → 最终文本
	resp = post(t, srv, `{"model":"m","tools":[{"type":"function","function":{"name":"lookup_metric"}}],
		"messages":[{"role":"user","content":"conn?"},{"role":"assistant","tool_calls":[]},{"role":"tool","content":"42"}]}`)
	var second struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&second)
	_ = resp.Body.Close()
	if second.Choices[0].FinishReason != "stop" {
		t.Fatalf("second turn = %+v", second.Choices[0])
	}
}

func TestFailModelReturns500(t *testing.T) {
	srv := httptest.NewServer(New(nil))
	defer srv.Close()
	resp := post(t, srv, `{"model":"mock-fail","messages":[{"role":"user","content":"hi"}]}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}

func TestResponsesEndpointIsNotImplemented(t *testing.T) {
	// 刻意不实现：LiteLLM 对 openai/ 前缀原样透传 /v1/responses，此处 404 就是"透传"的证据（T12 反例）。
	srv := httptest.NewServer(New(nil))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{}`))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
