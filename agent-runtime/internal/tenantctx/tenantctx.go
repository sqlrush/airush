// Package tenantctx 把租户身份贯穿到 agent 会话的每一层（spec-1.8 D3，AD-1/AD-10）：
// pgstore 的 RLS 事务、LLM 网关头（libs/llm.Meter 读 tenancy + CallInfo）、MCP 调用 `_meta`、
// 结构化日志字段。进程内不保存任何租户状态——租户只活在 ctx 里，会话 ctx 由 Session() 派生。
package tenantctx

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/sqlrush/airush/libs/llm"
	"github.com/sqlrush/airush/libs/obs"
	"github.com/sqlrush/airush/libs/tenancy"
)

// Info 是一条会话的归属：租户必填，其余尽力而为。
type Info struct {
	TenantID string `json:"tenant_id"`
	AgentID  string `json:"agent_id,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
}

type infoKey struct{}

// Session 由 parent 派生会话级 ctx：不继承 parent 的取消（会话寿命 > 一次 HTTP 请求），
// 携带租户（tenancy）、LLM 归属（llm.CallInfo：SessionID = thread id，Purpose = "agent"）
// 与带字段的 logger；info.TenantID 为空时 panic 前置于任何 I/O——这是编程错误不是运行时状态。
func Session(parent context.Context, info Info, logger *slog.Logger) context.Context {
	if info.TenantID == "" {
		panic("tenantctx.Session: tenant id is required")
	}
	ctx := context.WithoutCancel(parent)
	ctx = tenancy.WithTenant(ctx, info.TenantID)
	ctx = llm.WithCallInfo(ctx, llm.CallInfo{
		AgentID:   info.AgentID,
		SessionID: info.ThreadID,
		TraceID:   info.TraceID,
		Purpose:   "agent",
	})
	if logger == nil {
		logger = obs.LoggerFrom(parent)
	}
	ctx = obs.ContextWithLogger(ctx, logger.With(LogAttrs(info)...))
	return context.WithValue(ctx, infoKey{}, info)
}

// FromContext 取会话归属；未经 Session() 派生的 ctx 返回 false（租户可能仍在 tenancy 里）。
func FromContext(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(infoKey{}).(Info)
	if ok {
		return info, true
	}
	if tenantID, has := tenancy.FromContext(ctx); has {
		return Info{TenantID: tenantID}, false
	}
	return Info{}, false
}

// TenantID 取租户 id（tenancy 语义）。
func TenantID(ctx context.Context) (string, bool) { return tenancy.FromContext(ctx) }

// LogAttrs 是归属的日志字段（空值不出现）。
func LogAttrs(info Info) []any {
	attrs := []any{"tenant_id", info.TenantID}
	if info.AgentID != "" {
		attrs = append(attrs, "agent_id", info.AgentID)
	}
	if info.ThreadID != "" {
		attrs = append(attrs, "thread_id", info.ThreadID)
	}
	if info.TraceID != "" {
		attrs = append(attrs, "trace_id", info.TraceID)
	}
	return attrs
}

// mcpMeta 是 MCP 调用 `_meta.airush` 的形状（spec-1.8 §3.2；skill 侧据此打审计与配额）。
type mcpMeta struct {
	Airush Info `json:"airush"`
}

// MCPMeta 把归属渲染成 MCP `_meta`；无租户返回 nil（调用方应已 fail-closed，此处只保底）。
func MCPMeta(ctx context.Context) json.RawMessage {
	info, _ := FromContext(ctx)
	if info.TenantID == "" {
		return nil
	}
	raw, err := json.Marshal(mcpMeta{Airush: info})
	if err != nil {
		return nil
	}
	return raw
}
