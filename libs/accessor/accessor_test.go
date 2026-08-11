package accessor

import (
	"context"
	"testing"
)

func TestBuiltinDispatch(t *testing.T) {
	t.Parallel()

	ping := BuiltinDispatch(Command{ID: "1", Type: CommandPing})
	if ping.Status != StatusOK || ping.CommandID != "1" {
		t.Fatalf("ping = %+v", ping)
	}

	echo := BuiltinDispatch(Command{ID: "2", Type: CommandEcho, Payload: []byte("xy")})
	if echo.Status != StatusOK || string(echo.Payload) != "xy" {
		t.Fatalf("echo = %+v", echo)
	}

	// 动作类/未知 → 显式 unsupported + AR_COMMON_NOT_IMPLEMENTED（只读护栏，T9）
	for _, typ := range []string{"EXEC_SQL", "DROP_TABLE", "unknown"} {
		got := BuiltinDispatch(Command{ID: "3", Type: typ})
		if got.Status != StatusUnsupported || got.Code != "AR_COMMON_NOT_IMPLEMENTED" {
			t.Fatalf("type %q = %+v, want unsupported", typ, got)
		}
	}
}

// builtinAccessor 是最小 Accessor（校验接口可实现 + BuiltinDispatch 复用）。
type builtinAccessor struct{ closed bool }

func (a *builtinAccessor) Dispatch(_ context.Context, cmd Command) (Result, error) {
	return BuiltinDispatch(cmd), nil
}
func (a *builtinAccessor) Close() error { a.closed = true; return nil }

// 编译期断言：接口可被满足（T1 的编译期部分）。
var _ Accessor = (*builtinAccessor)(nil)

func TestAccessorInterface(t *testing.T) {
	t.Parallel()
	var a Accessor = &builtinAccessor{}
	res, err := a.Dispatch(context.Background(), Command{ID: "x", Type: CommandPing})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("dispatch = %+v err=%v", res, err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
