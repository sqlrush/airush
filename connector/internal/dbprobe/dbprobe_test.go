package dbprobe

import (
	"context"
	"testing"
	"time"

	"github.com/sqlrush/airush/libs/metrics"
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

func TestSnapshotKindForCommand(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		CommandProbeSlowlog: metrics.SnapshotKindSlowlog,
		CommandProbeSchema:  metrics.SnapshotKindSchema,
		CommandProbeConfig:  metrics.SnapshotKindConfig,
	}
	for command, want := range cases {
		got, ok := snapshotKindForCommand(command)
		if !ok || got != want {
			t.Fatalf("snapshotKindForCommand(%q) = %q,%v want %q", command, got, ok, want)
		}
		if !isSnapshotCommand(command) {
			t.Fatalf("%q should be a snapshot command", command)
		}
	}
	for _, command := range []string{CommandProbeMetrics, "PING", "PROBE_ROWDUMP", ""} {
		if _, ok := snapshotKindForCommand(command); ok {
			t.Fatalf("%q must not map to a snapshot kind", command)
		}
		if isSnapshotCommand(command) {
			t.Fatalf("%q must not be treated as a snapshot command", command)
		}
	}
}

// TestHandleSnapshotBadPayload：快照指令的 payload 非法 → 校验错误码，且不碰 pool。
func TestHandleSnapshotBadPayload(t *testing.T) {
	t.Parallel()
	h := &Handler{pool: nil}
	for _, command := range []string{CommandProbeSlowlog, CommandProbeSchema, CommandProbeConfig} {
		frame := h.Handle(context.Background(), &connectorv1.Command{
			CommandId: "c1", Type: command, Payload: []byte("not-json"),
		})
		res := frame.GetCommandResult()
		if res == nil || res.GetError().GetCode() != "AR_VALIDATION_FAILED" {
			t.Fatalf("%s bad payload = %+v", command, frame)
		}
	}
}

// TestHandleSnapshotUnknownKind：直接以非快照指令进入快照分支 → 显式拒绝码
// （AD-9 白名单在连接器侧的兜底，绝不静默处理）。
func TestHandleSnapshotUnknownKind(t *testing.T) {
	t.Parallel()
	h := &Handler{pool: nil}
	frame := h.handleSnapshot(context.Background(), &connectorv1.Command{
		CommandId: "c1", Type: "PROBE_ROWDUMP", Payload: []byte(`{"datasource_id":"ds1"}`),
	})
	res := frame.GetCommandResult()
	if res == nil || res.GetError().GetCode() != "AR_COLLECT_UNSUPPORTED_KIND" {
		t.Fatalf("unknown kind = %+v", frame)
	}
}

// TestHandleSnapshotUnknownEngine：引擎族无该 kind 目录 → 采集失败码。
func TestHandleSnapshotUnknownEngine(t *testing.T) {
	t.Parallel()
	h := &Handler{pool: nil}
	frame := h.handleSnapshot(context.Background(), &connectorv1.Command{
		CommandId: "c1", Type: CommandProbeConfig,
		Payload: []byte(`{"datasource_id":"ds1","engine_family":"mysql"}`),
	})
	res := frame.GetCommandResult()
	if res == nil || res.GetError().GetCode() != "AR_SNAPSHOT_COLLECT_FAILED" {
		t.Fatalf("unknown engine = %+v", frame)
	}
}

func TestUploadFrame(t *testing.T) {
	t.Parallel()
	cmd := &connectorv1.Command{CommandId: "c1", Type: CommandProbeConfig}
	frame := uploadFrame(cmd, "ds1", metrics.SnapshotKindConfig, metrics.Snapshot{Kind: metrics.SnapshotKindConfig})
	upload := frame.GetDataUpload()
	if upload == nil || upload.GetCommandId() != "c1" || upload.GetDatasourceId() != "ds1" ||
		upload.GetKind() != metrics.SnapshotKindConfig {
		t.Fatalf("upload frame = %+v", frame)
	}

	// 不可序列化载荷 → 内部错误码，不产出半截 DataUpload。
	bad := uploadFrame(cmd, "ds1", "metrics", make(chan int))
	if res := bad.GetCommandResult(); res == nil || res.GetError().GetCode() != "AR_INTERNAL_ERROR" {
		t.Fatalf("unmarshalable payload = %+v", bad)
	}
}

func TestStringifyValue(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 8, 12, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"text", "text"},
		{[]byte("bytes"), "bytes"},
		{true, "true"},
		{false, "false"},
		{ts, "2026-08-12T03:04:05Z"},
		{float64(1.5), "1.5"},
		{float32(2.5), "2.5"},
		{int64(7), "7"},
		{int32(8), "8"},
		{9, "9"},
		{uint8(3), "3"}, // 兜底走 fmt.Sprint
	}
	for _, tc := range cases {
		if got := stringifyValue(tc.in); got != tc.want {
			t.Fatalf("stringifyValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
