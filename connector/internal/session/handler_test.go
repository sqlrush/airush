package session

import (
	"context"
	"testing"

	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
)

func TestBuiltinHandler(t *testing.T) {
	t.Parallel()
	h := BuiltinHandler{}

	ping := h.Handle(context.Background(), &connectorv1.Command{CommandId: "1", Type: "PING"})
	if ping.GetStatus() != connectorv1.CommandResult_STATUS_OK || ping.GetCommandId() != "1" {
		t.Fatalf("ping = %+v", ping)
	}

	echo := h.Handle(context.Background(), &connectorv1.Command{CommandId: "2", Type: "ECHO", Payload: []byte("xy")})
	if echo.GetStatus() != connectorv1.CommandResult_STATUS_OK || string(echo.GetPayload()) != "xy" {
		t.Fatalf("echo = %+v", echo)
	}

	unk := h.Handle(context.Background(), &connectorv1.Command{CommandId: "3", Type: "NUKE"})
	if unk.GetStatus() != connectorv1.CommandResult_STATUS_UNSUPPORTED ||
		unk.GetError().GetCode() != "AR_COMMON_NOT_IMPLEMENTED" {
		t.Fatalf("unknown = %+v", unk)
	}
}

func TestMTLSCredsValidation(t *testing.T) {
	t.Parallel()
	if _, err := MTLSCreds([]byte("bad"), []byte("bad"), []byte("bad")); err == nil {
		t.Fatal("bad PEM accepted")
	}
}
