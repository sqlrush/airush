package dbprobe

import (
	"context"
	"testing"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

func TestHandlePassthroughNonMetrics(t *testing.T) {
	t.Parallel()
	// 非 PROBE_METRICS 指令 → nil（交由链上 BuiltinHandler），不触碰 pool。
	h := &Handler{pool: nil}
	if res := h.Handle(context.Background(), &connectorv1.Command{Type: "PING"}); res != nil {
		t.Fatalf("PING should pass through (nil), got %+v", res)
	}
	if res := h.Handle(context.Background(), &connectorv1.Command{Type: "ECHO"}); res != nil {
		t.Fatalf("ECHO should pass through (nil), got %+v", res)
	}
}

func TestHandleBadPayload(t *testing.T) {
	t.Parallel()
	h := &Handler{pool: nil}
	frame := h.Handle(context.Background(), &connectorv1.Command{
		CommandId: "c1", Type: CommandProbeMetrics, Payload: []byte("not-json"),
	})
	res := frame.GetCommandResult()
	if res == nil || res.GetStatus() != connectorv1.CommandResult_STATUS_ERROR ||
		res.GetError().GetCode() != "AR_VALIDATION_FAILED" {
		t.Fatalf("bad payload = %+v", frame)
	}
}
