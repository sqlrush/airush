package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sqlrush/codexgo/pkg/core"
	"github.com/sqlrush/codexgo/pkg/protocol"
	"github.com/sqlrush/codexgo/pkg/tools"

	"github.com/sqlrush/airush/agent-runtime/internal/approvals"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

func TestMergeMeta(t *testing.T) {
	airush := json.RawMessage(`{"airush":{"tenant_id":"t"}}`)
	if got := mergeMeta(nil, nil); got != nil {
		t.Fatalf("nil+nil = %s", got)
	}
	if got := mergeMeta(json.RawMessage(`{"x":1}`), nil); string(got) != `{"x":1}` {
		t.Fatalf("meta only = %s", got)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mergeMeta(json.RawMessage(`{"x":1,"airush":{"tenant_id":"spoofed"}}`), airush), &m); err != nil {
		t.Fatalf("merged: %v", err)
	}
	if string(m["x"]) != "1" || string(m["airush"]) != `{"tenant_id":"t"}` {
		t.Fatalf("merged = %v (airush must win over caller)", m)
	}
	if got := mergeMeta(json.RawMessage(`broken`), airush); string(got) != string(airush) {
		t.Fatalf("broken caller meta → airush only, got %s", got)
	}
}

func TestDeniedResultAndToolKind(t *testing.T) {
	res := deniedResult(approvals.Decision{Reason: "no"})
	if res.IsError == nil || !*res.IsError || len(res.Content) != 1 {
		t.Fatalf("denied result = %+v", res)
	}
	ro := json.RawMessage(`{"readOnlyHint":true}`)
	e := &Engine{mcpTools: []tools.McpToolInfo{
		{ServerName: "s", Tool: protocol.Tool{Name: "read", Annotations: &ro}},
		{ServerName: "s", Tool: protocol.Tool{Name: "write"}},
	}}
	if e.toolKind("s", "read") != approvals.KindRead || e.toolKind("s", "write") != approvals.KindAction || e.toolKind("s", "unknown") != approvals.KindAction || e.toolKind("other", "read") != approvals.KindAction {
		t.Fatal("toolKind classification")
	}
}

func TestNotifier(t *testing.T) {
	n := newNotifier()
	ch, unsub := n.Subscribe("k")
	n.Notify("k")
	n.Notify("k") // 合并，不阻塞
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("no notification")
	}
	n.Notify("other")
	select {
	case <-ch:
		t.Fatal("wrong key delivered")
	default:
	}
	unsub()
	n.Notify("k") // 退订后不 panic
	unsub()       // 幂等
}

func TestTenantOfAndConfigDefaults(t *testing.T) {
	if _, err := tenantOf(context.Background()); !isCodeUnit(err, apierror.CodeTenantContextMissing) {
		t.Fatalf("no tenant: %v", err)
	}
	if id, err := tenantOf(tenancy.WithTenant(context.Background(), "t1")); err != nil || id != "t1" {
		t.Fatalf("tenant = %q %v", id, err)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without store must fail")
	}
	if _, err := New(Config{Store: nil, LLMBaseURL: "x"}); err == nil {
		t.Fatal("New without transport must fail")
	}
}

func TestQueuedPayloadRoundTrip(t *testing.T) {
	raw, err := json.Marshal(queuedPayload{Type: queuedUserInput, Items: []protocol.UserInput{{Type: protocol.UserInputKindText, Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	var back queuedPayload
	if err := json.Unmarshal(raw, &back); err != nil || back.Type != queuedUserInput || len(back.Items) != 1 || back.Items[0].Text != "hi" {
		t.Fatalf("round trip = %+v (%v) raw=%s", back, err, raw)
	}
}

func isCodeUnit(err error, code apierror.Code) bool {
	ae, ok := apierror.FromError(err)
	return ok && ae.Code == code
}

func TestStaticModelsAndDenyReviewer(t *testing.T) {
	m := staticModels{defaultSlug: "chat-default"}
	if m.DefaultModelSlug() != "chat-default" {
		t.Fatal("default slug")
	}
	info, err := m.ModelInfo(context.Background(), "")
	if err != nil || info == nil {
		t.Fatalf("model info = %v (%v)", info, err)
	}
	d := denyReviewer{}.ReviewApproval(context.Background(), nil, core.ApprovalAction{}, nil, nil)
	if d.Kind != protocol.ReviewDecisionDenied || d.Rejection == nil {
		t.Fatalf("deny reviewer = %+v", d)
	}
}
