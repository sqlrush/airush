//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/tools"

	"github.com/sqlrush/airush/agent-runtime/internal/approvals"
	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
)

// fakeMCP 是 MCPGateway 的假实现：两个工具（只读 lookup_metric、动作类 restart_db），记录调用与 _meta。
type fakeMCP struct {
	mu    sync.Mutex
	calls []fakeMCPCall
}

type fakeMCPCall struct {
	Qualified string
	Meta      json.RawMessage
}

func (f *fakeMCP) ListAllToolInfos() []tools.McpToolInfo {
	ro := json.RawMessage(`{"readOnlyHint":true}`)
	return []tools.McpToolInfo{
		{ServerName: "skills", CallableName: "lookup_metric", CallableNamespace: "mcp__skills__", Tool: protocol.Tool{Name: "lookup_metric", InputSchema: json.RawMessage(`{"type":"object"}`), Annotations: &ro}},
		{ServerName: "skills", CallableName: "restart_db", CallableNamespace: "mcp__skills__", Tool: protocol.Tool{Name: "restart_db", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
}

func (f *fakeMCP) CallQualifiedTool(_ context.Context, qualified string, _ json.RawMessage, meta json.RawMessage) (protocol.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeMCPCall{Qualified: qualified, Meta: meta})
	f.mu.Unlock()
	text, _ := json.Marshal(map[string]string{"type": "text", "text": `{"cpu":12}`})
	return protocol.CallToolResult{Content: []json.RawMessage{text}}, nil
}

func (f *fakeMCP) snapshot() []fakeMCPCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMCPCall(nil), f.calls...)
}

// TestApprovalStageDeniesActionTools spec-1.8 T9：模型调用动作类工具 → 审批阶段拒绝 → 两条审批事件
// （approval_requested + approval_denied）入事件流 → 工具结果是 isError 的拒绝说明 → 后端 MCP 没被调用；
// 只读工具直放且 _meta 带租户。
func TestApprovalStageDeniesActionTools(t *testing.T) {
	ctx, tenantID := newTenant(t)
	llmSrv := newFakeLLM(t)
	llmSrv.ToolCall = "restart_db"
	gw := &fakeMCP{}
	e, _ := newEngine(t, llmSrv, "pod-a", func(c *Config) { c.MCP = gw })
	ref, _ := e.StartThread(ctx, StartThreadInput{})
	if _, err := e.SubmitTurn(ctx, ref.ThreadID, textInput("重启数据库")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, ctx, ref.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)

	types := eventTypes(t, ctx, ref.ThreadID)
	if !contains(types, approvals.EventApprovalRequested) || !contains(types, approvals.EventApprovalDenied) {
		t.Fatalf("approval events missing: %v", types)
	}
	if len(gw.snapshot()) != 0 {
		t.Fatalf("action tool reached the MCP backend: %+v", gw.snapshot())
	}
	// 第二次采样携带工具输出 = 拒绝说明
	reqs := llmSrv.requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want the follow-up sampling after the tool call", len(reqs))
	}
	raw, _ := json.Marshal(reqs[1])
	if !strings.Contains(string(raw), "approval denied") {
		t.Fatalf("model did not see the denial: %s", raw[:min(len(raw), 800)])
	}
	// 事件 payload：kind=action、工具名、decided_by
	evs, _ := testStore.ReadEvents(ctx, protocol.NewThreadID(ref.ThreadID), 0, 0)
	for _, ev := range evs {
		if ev.EventType != approvals.EventApprovalDenied {
			continue
		}
		var p struct {
			Kind      string `json:"kind"`
			Tool      string `json:"tool"`
			DecidedBy string `json:"decided_by"`
			Reason    string `json:"reason"`
		}
		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(ev.Payload, &env); err != nil {
			t.Fatalf("event envelope: %v", err)
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("event payload: %v (%s)", err, ev.Payload)
		}
		if p.Kind != string(approvals.KindAction) || p.Tool != "restart_db" || p.DecidedBy != approvals.DecidedByStage1Deny || p.Reason == "" {
			t.Fatalf("denied payload = %+v", p)
		}
	}

	// 只读工具直放：_meta.airush 带租户与线程
	llmSrv2 := newFakeLLM(t)
	llmSrv2.ToolCall = "lookup_metric"
	gw2 := &fakeMCP{}
	e2, _ := newEngine(t, llmSrv2, "pod-a", func(c *Config) { c.MCP = gw2 })
	ref2, _ := e2.StartThread(ctx, StartThreadInput{})
	if _, err := e2.SubmitTurn(ctx, ref2.ThreadID, textInput("看指标")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitStatus(t, ctx, ref2.ThreadID, pgstore.ThreadStatusIdle, 15*time.Second)
	calls := gw2.snapshot()
	if len(calls) != 1 || calls[0].Qualified != "mcp__skills__lookup_metric" {
		t.Fatalf("read tool calls = %+v", calls)
	}
	var meta struct {
		Airush struct {
			TenantID string `json:"tenant_id"`
			ThreadID string `json:"thread_id"`
		} `json:"airush"`
	}
	if err := json.Unmarshal(calls[0].Meta, &meta); err != nil || meta.Airush.TenantID != tenantID || meta.Airush.ThreadID != ref2.ThreadID {
		t.Fatalf("mcp _meta = %s (%v), want tenant %s thread %s", calls[0].Meta, err, tenantID, ref2.ThreadID)
	}
	types2 := eventTypes(t, ctx, ref2.ThreadID)
	if !contains(types2, approvals.EventApprovalRequested) || contains(types2, approvals.EventApprovalDenied) {
		t.Fatalf("read tool events = %v (want requested without denied)", types2)
	}
	if !contains(types2, "mcp_tool_call_end") {
		t.Fatalf("mcp_tool_call_end not persisted: %v", types2)
	}
}
