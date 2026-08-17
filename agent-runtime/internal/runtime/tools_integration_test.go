//go:build integration

package runtime

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/llm"
	"github.com/sqlrush/airush/testkit/mockllm"
)

// TestAdvertisedToolsArePlatformShaped：无本地文件系统/托管搜索——只广告 update_plan（+ MCP 工具、
// tool_search 家族），不广告 view_image / web_search（Kimi 金丝雀实测曾看到两者）。
func TestAdvertisedToolsArePlatformShaped(t *testing.T) {
	ctx, _ := newTenant(t)
	llmSrv := newFakeLLM(t)
	e, _ := newEngine(t, llmSrv, "pod-a")
	ref, _ := e.StartThread(ctx, StartThreadInput{})
	if _, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("hi")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 10*time.Second)
	raw, _ := json.Marshal(llmSrv.requests()[0]["tools"])
	tools := string(raw)
	if strings.Contains(tools, `"view_image"`) || strings.Contains(tools, `"web_search"`) {
		t.Fatalf("platform must not advertise view_image/web_search: %s", tools[:min(len(tools), 600)])
	}
	if !strings.Contains(tools, `"update_plan"`) {
		t.Fatalf("update_plan missing: %s", tools[:min(len(tools), 600)])
	}
}

// TestChatWireRoundTrip：缺省线协议 chat（spec-1.7 Q3 备选 B）走 testkit/mockllm（chat/completions，
// 带工具回合）：一轮里模型先回 tool_call（lookup_metric，未注册 → core 回错误输出），再回最终文本；
// function_call / function_call_output 落事件流，Meter 从 chat 响应提到 usage。
func TestChatWireRoundTrip(t *testing.T) {
	ctx, _ := newTenant(t)
	mock := httptest.NewServer(mockllm.New(nil))
	t.Cleanup(mock.Close)
	stubs := &meterStubs{}
	meter := llm.NewMeter(nil, stubs, stubs, llm.WithMasterKey("test-key"))
	e, err := New(Config{
		Store: testStore, LLMBaseURL: mock.URL + "/v1", LLMTransport: meter, PodName: "pod-chat",
		LLMWireAPI: WireAPIChat, HeartbeatInterval: 200 * time.Millisecond, Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	ref, _ := e.StartThread(ctx, StartThreadInput{})
	if _, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("看看连接数")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	evs, _ := testStore.ReadEvents(ctx, protocol.NewThreadID(ref.ThreadID), 0, 0)
	var calls, outs, msgs int
	for _, ev := range evs {
		p := string(ev.Payload)
		switch {
		case ev.EventType == pgstore.EventTypeResponseItem && strings.Contains(p, `"function_call_output"`):
			outs++
		case ev.EventType == pgstore.EventTypeResponseItem && strings.Contains(p, `"function_call"`):
			calls++
		case ev.EventType == "agent_message" && strings.Contains(p, mockllm.ReplyText):
			msgs++
		}
	}
	if calls != 1 || outs != 1 || msgs != 1 {
		t.Fatalf("chat wire round: function_call=%d output=%d agent_message=%d events=%v", calls, outs, msgs, eventTypes(t, ctx, ref.ThreadID))
	}
	if stubs.count() < 2 || stubs.records[0].TotalTokens != mockllm.TotalTokens {
		t.Fatalf("meter records = %d first=%+v", stubs.count(), stubs.records)
	}
}
