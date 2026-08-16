// Package mockllm 是一个 OpenAI Chat Completions 兼容的**假供应商**（spec-1.7 D6）。
//
// 两处用它：① libs/llm 集成测试里进程内起一个，让 testcontainers 起的真 LiteLLM 打它；
// ② dev 环境作为 LiteLLM 的 sidecar（values-dev `llm.mockProvider.enabled`），dev-verify
// 不必持有任何真实供应商 key 就能验"网关路由通"。
//
// 它故意只实现最小面：/v1/models、/v1/chat/completions（流式/非流式、工具调用回合）。
// 不实现 /v1/responses——这正是 T12 的反例所需：LiteLLM 对 openai/ 前缀会把 Responses
// 原样透传过来，此处 404 即"透传"的证据。
package mockllm

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// 固定用量：测试断言用常量比对，比按内容估算稳定。
const (
	PromptTokens     = 11
	CompletionTokens = 3
	TotalTokens      = PromptTokens + CompletionTokens

	// ReplyText 是非工具回合的固定回复。
	ReplyText = "mock reply"
	// FailModelSubstring：模型名含此子串 → 恒回 500（T13 fallback 用）。
	FailModelSubstring = "fail"
	// ToolName 是工具回合里返回的 tool_call 函数名。
	ToolName = "lookup_metric"
)

// Handler 是假供应商的 http.Handler；Requests 计数供测试断言"打到了几次"。
type Handler struct {
	Requests atomic.Int64
	logger   *slog.Logger
}

// New 构造；logger 可为 nil（静默）。
func New(logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Handler{logger: logger}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.Requests.Add(1)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		h.models(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
		h.chat(w, r)
	default:
		// 含 /v1/responses：不实现，让透传行为原形毕露。
		h.logger.Info("mockllm unsupported", "method", r.Method, "path", r.URL.Path)
		http.NotFound(w, r)
	}
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	h.logger.Info("mockllm models", "authed", r.Header.Get("Authorization") != "")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mock-model","object":"model","owned_by":"mock"}]}`))
}

// chatRequest 只解析决定行为所需的字段；其余原样忽略（LiteLLM 会补一堆参数）。
type chatRequest struct {
	Model         string            `json:"model"`
	Stream        bool              `json:"stream"`
	StreamOptions *json.RawMessage  `json:"stream_options"`
	Messages      []json.RawMessage `json:"messages"`
	Tools         []json.RawMessage `json:"tools"`
}

func (h *Handler) chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	lastRole := lastMessageRole(req.Messages)
	h.logger.Info("mockllm chat", "model", req.Model, "stream", req.Stream,
		"stream_options", rawOr(req.StreamOptions), "msgs", len(req.Messages),
		"tools", len(req.Tools), "last_role", lastRole, "authed", r.Header.Get("Authorization") != "")

	if strings.Contains(req.Model, FailModelSubstring) {
		// 上游 5xx：给 LiteLLM 的 fallback 与我们的错误映射一个真实的失败源。
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"mock upstream failure","type":"server_error"}}`))
		return
	}

	// 工具回合：带 tools 且最后一条是 user → 回 tool_call；最后一条是 tool → 回最终文本。
	wantToolCall := len(req.Tools) > 0 && lastRole == "user"
	if req.Stream {
		h.streamChat(w, req.Model, wantToolCall)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	msg := `{"role":"assistant","content":` + jsonString(ReplyText) + `}`
	finish := "stop"
	if wantToolCall {
		msg = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_mock_1","type":"function","function":{"name":"` + ToolName + `","arguments":"{\"name\":\"db.connections.active\"}"}}]}`
		finish = "tool_calls"
	}
	_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-mock","object":"chat.completion","created":%d,"model":%q,`+
		`"choices":[{"index":0,"message":%s,"finish_reason":%q}],`+
		`"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`,
		time.Now().Unix(), req.Model, msg, finish, PromptTokens, CompletionTokens, TotalTokens)
}

// streamChat 按 OpenAI SSE 形态吐流；末帧无条件带 usage（真实供应商只在
// stream_options.include_usage 时带——Meter 会强制加该参数，此处不区分以简化）。
func (h *Handler) streamChat(w http.ResponseWriter, model string, toolCall bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	chunk := func(delta, finish string) {
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,"+
			"\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":%s}]}\n\n", model, delta, finish)
		flush()
	}
	if toolCall {
		chunk(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_mock_1","type":"function","function":{"name":"`+ToolName+`","arguments":""}}]}`, "null")
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"name\":\"db.connections.active\"}"}}]}`, "null")
		chunk(`{}`, `"tool_calls"`)
	} else {
		chunk(`{"role":"assistant","content":"mock"}`, "null")
		chunk(`{"content":" reply"}`, "null")
		chunk(`{}`, `"stop"`)
	}
	_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"model\":%q,\"choices\":[],"+
		"\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n",
		model, PromptTokens, CompletionTokens, TotalTokens)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

func lastMessageRole(msgs []json.RawMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	var m struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(msgs[len(msgs)-1], &m)
	return m.Role
}

func rawOr(r *json.RawMessage) string {
	if r == nil {
		return "null"
	}
	return string(*r)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
