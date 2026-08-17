package approvals

import (
	"encoding/json"
	"time"

	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/rollout"
)

// 审批事件是 airush runtime 自有的两种事件类型（spec-1.8 §3.6），与 codexgo 事件同一条流：
// 以 EventMsg 的 forward-compat 形态（Type + Raw）承载，pgstore 白名单显式放行这两个名字。
const (
	// EventApprovalRequested 记"某动作类工具请求了审批"。
	EventApprovalRequested = "approval_requested"
	// EventApprovalDenied 记"审批阶段拒绝了该调用"（Stage 1 唯一结论）。
	EventApprovalDenied = "approval_denied"
)

// approvalEvent 是两条事件共用的 payload。
type approvalEvent struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Qualified string          `json:"qualified_name"`
	Kind      Kind            `json:"kind"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	DecidedBy string          `json:"decided_by,omitempty"`
	AtMs      int64           `json:"at_ms"`
}

// Events 把一次审批结论渲染成 rollout 项：requested 恒有，denied 仅当拒绝。
func Events(call ToolCall, d Decision, now time.Time) []rollout.RolloutItem {
	base := approvalEvent{
		CallID: call.CallID, TurnID: call.TurnID, Server: call.Server, Tool: call.Tool,
		Qualified: call.Qualified, Kind: call.Kind, Arguments: call.Arguments, AtMs: now.UnixMilli(),
	}
	requested := base
	requested.Type = EventApprovalRequested
	items := []rollout.RolloutItem{eventItem(requested)}
	if !d.Allowed {
		denied := base
		denied.Type = EventApprovalDenied
		denied.Reason = d.Reason
		denied.DecidedBy = d.DecidedBy
		items = append(items, eventItem(denied))
	}
	return items
}

func eventItem(ev approvalEvent) rollout.RolloutItem {
	raw, _ := json.Marshal(ev)
	return rollout.NewEventMsgItem(protocol.EventMsg{Type: protocol.EventMsgKind(ev.Type), Raw: raw})
}

// IsRuntimeEvent 判定 event type 是否是 runtime 自有事件（pgstore 白名单用）。
func IsRuntimeEvent(eventType string) bool {
	return eventType == EventApprovalRequested || eventType == EventApprovalDenied
}
