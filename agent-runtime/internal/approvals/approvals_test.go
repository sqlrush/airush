package approvals

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/rollout"
)

func TestKindOfFailsClosed(t *testing.T) {
	cases := map[string]Kind{
		``:                                  KindAction,
		`{"readOnlyHint":true}`:             KindRead,
		`{"readOnlyHint":false}`:            KindAction,
		`{"destructiveHint":true}`:          KindAction,
		`{"readOnlyHint":"yes"}`:            KindAction,
		`not json`:                          KindAction,
		`{"readOnlyHint":true,"x":"y"}`:     KindRead,
		`{"title":"t","readOnlyHint":true}`: KindRead,
	}
	for raw, want := range cases {
		if got := KindOf(json.RawMessage(raw)); got != want {
			t.Fatalf("KindOf(%s) = %s, want %s", raw, got, want)
		}
	}
}

func TestDenyActions(t *testing.T) {
	var a Approver = DenyActions{}
	read := a.Review(context.Background(), ToolCall{Kind: KindRead, Tool: "lookup"})
	if !read.Allowed || read.DecidedBy != DecidedByReadOnly {
		t.Fatalf("read = %+v", read)
	}
	action := a.Review(context.Background(), ToolCall{Kind: KindAction, Tool: "restart"})
	if action.Allowed || action.Reason == "" || action.DecidedBy != DecidedByStage1Deny {
		t.Fatalf("action = %+v", action)
	}
	unknown := a.Review(context.Background(), ToolCall{Tool: "mystery"})
	if unknown.Allowed {
		t.Fatalf("unknown kind must be denied: %+v", unknown)
	}
}

func TestEventsShape(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	call := ToolCall{Qualified: "mcp__s__restart", Server: "s", Tool: "restart", Kind: KindAction, CallID: "c1", TurnID: "t1", Arguments: json.RawMessage(`{"a":1}`)}
	denied := Events(call, DenyActions{}.Review(context.Background(), call), now)
	if len(denied) != 2 || denied[0].Kind != rollout.RolloutItemKindEventMsg || string(denied[0].EventMsg.Type) != EventApprovalRequested || string(denied[1].EventMsg.Type) != EventApprovalDenied {
		t.Fatalf("denied events = %+v", denied)
	}
	var payload map[string]any
	if err := json.Unmarshal(denied[1].EventMsg.Raw, &payload); err != nil {
		t.Fatalf("raw: %v", err)
	}
	if payload["type"] != EventApprovalDenied || payload["tool"] != "restart" || payload["kind"] != "action" || payload["decided_by"] != DecidedByStage1Deny || payload["at_ms"] != float64(now.UnixMilli()) {
		t.Fatalf("payload = %v", payload)
	}
	// forward-compat 形态：EventMsg 序列化后 type 与 Raw 一致（pgstore 白名单靠这个）
	raw, err := json.Marshal(denied[1].EventMsg)
	if err != nil || string(raw) != string(denied[1].EventMsg.Raw) {
		t.Fatalf("marshal = %s (%v)", raw, err)
	}
	allowed := Events(ToolCall{Kind: KindRead, Tool: "lookup"}, Decision{Allowed: true, DecidedBy: DecidedByReadOnly}, now)
	if len(allowed) != 1 || string(allowed[0].EventMsg.Type) != EventApprovalRequested {
		t.Fatalf("allowed events = %+v", allowed)
	}
	if !IsRuntimeEvent(EventApprovalRequested) || !IsRuntimeEvent(EventApprovalDenied) || IsRuntimeEvent("agent_message") {
		t.Fatal("IsRuntimeEvent")
	}
}
