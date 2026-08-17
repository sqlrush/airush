package tenantctx

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sqlrush/airush/libs/llm"
	"github.com/sqlrush/airush/libs/tenancy"
)

func TestSessionCarriesTenantAndCallInfoWithoutCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	info := Info{TenantID: "t1", AgentID: "a1", ThreadID: "th1", TraceID: "tr1"}
	ctx := Session(parent, info, nil)
	cancel()
	if ctx.Err() != nil {
		t.Fatal("session ctx must not inherit parent cancellation")
	}
	if id, ok := tenancy.FromContext(ctx); !ok || id != "t1" {
		t.Fatalf("tenant = %q %v", id, ok)
	}
	ci := llm.CallInfoFrom(ctx)
	if ci.AgentID != "a1" || ci.SessionID != "th1" || ci.TraceID != "tr1" || ci.Purpose != "agent" {
		t.Fatalf("call info = %+v", ci)
	}
	got, ok := FromContext(ctx)
	if !ok || got != info {
		t.Fatalf("FromContext = %+v %v", got, ok)
	}
	if id, ok := TenantID(ctx); !ok || id != "t1" {
		t.Fatalf("TenantID = %q %v", id, ok)
	}
}

func TestFromContextFallsBackToTenancy(t *testing.T) {
	ctx := tenancy.WithTenant(context.Background(), "t2")
	info, ok := FromContext(ctx)
	if ok || info.TenantID != "t2" {
		t.Fatalf("fallback = %+v %v", info, ok)
	}
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("empty ctx must not report a session")
	}
}

func TestMCPMeta(t *testing.T) {
	if MCPMeta(context.Background()) != nil {
		t.Fatal("no tenant → nil meta")
	}
	ctx := Session(context.Background(), Info{TenantID: "t1", ThreadID: "th1"}, nil)
	var m struct {
		Airush Info `json:"airush"`
	}
	if err := json.Unmarshal(MCPMeta(ctx), &m); err != nil || m.Airush.TenantID != "t1" || m.Airush.ThreadID != "th1" {
		t.Fatalf("meta = %s (%v)", MCPMeta(ctx), err)
	}
	if attrs := LogAttrs(Info{TenantID: "t"}); len(attrs) != 2 {
		t.Fatalf("attrs = %v", attrs)
	}
	if attrs := LogAttrs(Info{TenantID: "t", AgentID: "a", ThreadID: "th", TraceID: "tr"}); len(attrs) != 8 {
		t.Fatalf("attrs = %v", attrs)
	}
}

func TestSessionPanicsWithoutTenant(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	Session(context.Background(), Info{}, nil)
}
