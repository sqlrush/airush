// Package approvals 是 AD-9 审批阶段在 agent-runtime 里的落点（spec-1.8 D6）。
//
// 工具路由（runtime 的 MCP caller 包装）**只认本包的返回**：动作类工具（kind=action）进入
// Approver；Stage 1 的实现是"一律拒绝并写两条审批事件"（无令牌流——令牌流与人审工作流在
// spec-2.5），只读工具直放。因此不存在"动作类工具绕过审批阶段"的路径：核心里没有装配任何
// 本地执行器（shell/apply_patch 为空），唯一能触达客户系统的通道就是 MCP，而 MCP 调用必经这里。
package approvals

import (
	"context"
	"encoding/json"
)

// Kind 是工具的读写分类（工具目录字段；spec-1.9 注册表定版前来自 MCP tool annotations）。
type Kind string

const (
	// KindRead 只读工具：直放。
	KindRead Kind = "read"
	// KindAction 动作类工具：必须过审批阶段。
	KindAction Kind = "action"
)

// ToolCall 是待审的一次工具调用。
type ToolCall struct {
	// Qualified 是 core 侧的规范名（mcp__<server>__<tool>）；Server/Tool 是拆开的两段。
	Qualified string
	Server    string
	Tool      string
	Kind      Kind
	// CallID 是本次调用 id（core 传入的 call_id，写审批事件用；可空）。
	CallID string
	// TurnID 是当前 turn（可空）。
	TurnID string
	// Arguments 是调用参数原文（只进事件 payload，不改写）。
	Arguments json.RawMessage
}

// Decision 是审批阶段的结论。
type Decision struct {
	Allowed bool
	// Reason 给模型看的拒绝理由（英文，模型据此换只读路径或向用户说明）。
	Reason string
	// DecidedBy 记录谁做的决定（stage1-deny / read-only-passthrough）。
	DecidedBy string
}

// Approver 是审批阶段接口；换实现（spec-2.5 令牌流）只换这里。
type Approver interface {
	Review(ctx context.Context, call ToolCall) Decision
}

// DecidedByStage1Deny / DecidedByReadOnly 是 Stage 1 的两个决定来源。
const (
	DecidedByStage1Deny = "stage1-deny"
	DecidedByReadOnly   = "read-only-passthrough"
)

// DenyActions 是 Stage 1 实现：动作类一律拒，只读直放。
type DenyActions struct{}

// Review 实现 Approver。
func (DenyActions) Review(_ context.Context, call ToolCall) Decision {
	if call.Kind == KindRead {
		return Decision{Allowed: true, DecidedBy: DecidedByReadOnly}
	}
	return Decision{
		Allowed:   false,
		Reason:    "action tools require human approval, which is not available in this deployment stage; use read-only tools or ask the user to run the action",
		DecidedBy: DecidedByStage1Deny,
	}
}

var _ Approver = DenyActions{}

// KindOf 由 MCP tool annotations 判定读写分类：`readOnlyHint: true` 为只读，其余
// （含无 annotations、destructiveHint、解析失败）一律动作类——fail-closed。
func KindOf(annotations json.RawMessage) Kind {
	if len(annotations) == 0 {
		return KindAction
	}
	var probe struct {
		ReadOnlyHint *bool `json:"readOnlyHint"`
	}
	if err := json.Unmarshal(annotations, &probe); err != nil || probe.ReadOnlyHint == nil || !*probe.ReadOnlyHint {
		return KindAction
	}
	return KindRead
}
