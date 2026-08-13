package session

import (
	"context"
	"testing"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

func TestBuiltinHandler(t *testing.T) {
	t.Parallel()
	h := BuiltinHandler{}

	ping := h.Handle(context.Background(), &connectorv1.Command{CommandId: "1", Type: "PING"}).GetCommandResult()
	if ping.GetStatus() != connectorv1.CommandResult_STATUS_OK || ping.GetCommandId() != "1" {
		t.Fatalf("ping = %+v", ping)
	}

	echo := h.Handle(context.Background(), &connectorv1.Command{CommandId: "2", Type: "ECHO", Payload: []byte("xy")}).GetCommandResult()
	if echo.GetStatus() != connectorv1.CommandResult_STATUS_OK || string(echo.GetPayload()) != "xy" {
		t.Fatalf("echo = %+v", echo)
	}

	unk := h.Handle(context.Background(), &connectorv1.Command{CommandId: "3", Type: "NUKE"}).GetCommandResult()
	if unk.GetStatus() != connectorv1.CommandResult_STATUS_UNSUPPORTED ||
		unk.GetError().GetCode() != "AR_COMMON_NOT_IMPLEMENTED" {
		t.Fatalf("unknown = %+v", unk)
	}
}

// stubHandler 按脚本返回帧或 nil（透传）。
type stubHandler struct{ frame *connectorv1.ClientFrame }

func (s stubHandler) Handle(context.Context, *connectorv1.Command) *connectorv1.ClientFrame {
	return s.frame
}

func TestChainHandler(t *testing.T) {
	t.Parallel()
	cmd := &connectorv1.Command{CommandId: "1", Type: "PING"}

	// 前置 handler 透传（nil）→ 落到 BuiltinHandler，PING 得 OK。
	chain := ChainHandler{stubHandler{frame: nil}}
	if res := chain.Handle(context.Background(), cmd).GetCommandResult(); res.GetStatus() != connectorv1.CommandResult_STATUS_OK {
		t.Fatalf("passthrough→builtin PING = %+v", res)
	}

	// 前置 handler 命中 → 直接返回其帧，不再兜底。
	own := &connectorv1.ClientFrame{Frame: &connectorv1.ClientFrame_CommandResult{
		CommandResult: &connectorv1.CommandResult{CommandId: "1", Status: connectorv1.CommandResult_STATUS_UNSUPPORTED},
	}}
	chain = ChainHandler{stubHandler{frame: own}}
	if res := chain.Handle(context.Background(), cmd).GetCommandResult(); res.GetStatus() != connectorv1.CommandResult_STATUS_UNSUPPORTED {
		t.Fatalf("front handler hit not honored = %+v", res)
	}

	// 空链兜底到 BuiltinHandler。
	if res := (ChainHandler{}).Handle(context.Background(), cmd).GetCommandResult(); res.GetStatus() != connectorv1.CommandResult_STATUS_OK {
		t.Fatalf("empty chain fallback = %+v", res)
	}
}

func TestMTLSCredsValidation(t *testing.T) {
	t.Parallel()
	if _, err := MTLSCreds([]byte("bad"), []byte("bad"), []byte("bad")); err == nil {
		t.Fatal("bad PEM accepted")
	}
}
