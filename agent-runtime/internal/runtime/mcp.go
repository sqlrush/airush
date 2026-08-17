package runtime

import (
	"context"
	"encoding/json"

	"github.com/sqlrush/codexgo/pkg/mcp"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/tools"

	"github.com/sqlrush/airush/agent-runtime/internal/approvals"
	"github.com/sqlrush/airush/agent-runtime/internal/tenantctx"
)

// gatedMcpCaller 是 core.McpToolCaller 的平台实现：每次 MCP 调用先过审批阶段（AD-9），
// 再注入租户 `_meta`，最后才到 MCP 管理器。core 的工具路由只认这个 caller，没有第二条通道。
type gatedMcpCaller struct {
	engine   *Engine
	threadID protocol.ThreadID
}

// CallQualifiedTool 实现 core.McpToolCaller。
func (g *gatedMcpCaller) CallQualifiedTool(ctx context.Context, qualifiedName string, arguments, meta json.RawMessage) (protocol.CallToolResult, error) {
	server, tool, err := mcp.ParseFullyQualifiedToolName(qualifiedName)
	if err != nil {
		return protocol.CallToolResult{}, err
	}
	call := approvals.ToolCall{
		Qualified: qualifiedName, Server: server, Tool: tool,
		Kind:      g.engine.toolKind(server, tool),
		TurnID:    g.engine.currentTurn(g.threadID),
		Arguments: arguments,
	}
	decision := g.engine.approver.Review(ctx, call)
	observeApproval(ctx, decision.Allowed)
	g.engine.recordApproval(ctx, g.threadID, call, decision)
	if !decision.Allowed {
		return deniedResult(decision), nil
	}
	return g.engine.cfg.MCP.CallQualifiedTool(ctx, qualifiedName, arguments, mergeMeta(meta, tenantctx.MCPMeta(ctx)))
}

// toolKind 由已发现的工具目录判读写分类；目录里没有的工具按动作类处理（fail-closed）。
func (e *Engine) toolKind(server, tool string) approvals.Kind {
	for _, info := range e.mcpTools {
		if info.ServerName == server && info.Tool.Name == tool {
			var ann json.RawMessage
			if info.Tool.Annotations != nil {
				ann = *info.Tool.Annotations
			}
			return approvals.KindOf(ann)
		}
	}
	return approvals.KindAction
}

// recordApproval 把审批事件写进线程事件流（写失败只记日志：审批结论本身已经生效，
// 事件是审计而不是门）。
func (e *Engine) recordApproval(ctx context.Context, threadID protocol.ThreadID, call approvals.ToolCall, d approvals.Decision) {
	items := approvals.Events(call, d, e.now())
	if _, err := e.store.AppendRolloutItems(ctx, threadID, items); err != nil {
		e.logger.Warn("record approval events failed", "thread_id", threadID.String(), "tool", call.Qualified, "error", err)
		return
	}
	e.notifier.Notify(threadID.String())
}

// deniedResult 把拒绝渲染成模型可读的 MCP 错误结果（isError=true），模型据此改走只读路径。
func deniedResult(d approvals.Decision) protocol.CallToolResult {
	text, _ := json.Marshal(map[string]string{"type": "text", "text": "approval denied: " + d.Reason})
	isErr := true
	return protocol.CallToolResult{Content: []json.RawMessage{text}, IsError: &isErr}
}

// mergeMeta 把租户 `_meta.airush` 并进调用方的 _meta（调用方 _meta 里的同名键被覆盖——租户身份
// 只能来自 ctx）。
func mergeMeta(meta, airush json.RawMessage) json.RawMessage {
	if len(airush) == 0 {
		return meta
	}
	merged := map[string]json.RawMessage{}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &merged)
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(airush, &extra); err != nil {
		return meta
	}
	for k, v := range extra {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return meta
	}
	return out
}

// currentTurn 返回线程在本 pod 上正在执行的 turn id（无 → 空）。
func (e *Engine) currentTurn(threadID protocol.ThreadID) string {
	e.mu.Lock()
	lt := e.live[threadID.String()]
	e.mu.Unlock()
	if lt == nil {
		return ""
	}
	return lt.turnID()
}

// MCPGateway 是运行时需要的 MCP 管理器最小面（*mcp.Manager 满足；测试注入假实现）。
type MCPGateway interface {
	ListAllToolInfos() []tools.McpToolInfo
	CallQualifiedTool(ctx context.Context, qualifiedName string, arguments, meta json.RawMessage) (protocol.CallToolResult, error)
}
