//go:build integration

// spec-1.3 D4/T11（连接器侧）：dbprobe 对真实 PG 执行探针，PROBE_METRICS → DataUpload
// 帧携带结构化 batch；只读、无原始行数据（AD-3）。
package dbprobe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sqlrush/airush/libs/metrics"
	connectorv1 "github.com/sqlrush/airush/proto/gen/go/connector/v1"
	"github.com/sqlrush/airush/testkit"
)

func TestDBProbeHandleProducesDataUpload(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	h, err := New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("new probe: %v", err)
	}
	t.Cleanup(h.Close)

	payload, _ := json.Marshal(probeRequest{DatasourceID: "ds1", EngineFamily: "postgres"})
	frame := h.Handle(ctx, &connectorv1.Command{CommandId: "c1", Type: CommandProbeMetrics, Payload: payload})

	du := frame.GetDataUpload()
	if du == nil {
		t.Fatalf("expected DataUpload frame, got %+v", frame)
	}
	if du.GetCommandId() != "c1" || du.GetKind() != DataUploadKindMetrics {
		t.Fatalf("data upload meta = %+v", du)
	}
	var batch metrics.Batch
	if err := json.Unmarshal(du.GetPayload(), &batch); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if batch.DatasourceID != "ds1" || len(batch.Metrics) == 0 {
		t.Fatalf("batch = %+v", batch)
	}
	// 值合理域抽样（T2）：缓存命中率 ∈ [0,1]。
	for _, m := range batch.Metrics {
		if m.Name == "db.cache.hit_ratio" && (m.Value < 0 || m.Value > 1) {
			t.Fatalf("hit_ratio out of domain: %v", m.Value)
		}
	}
}

func TestDBProbeUnsupportedPassthrough(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	h, err := New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("new probe: %v", err)
	}
	t.Cleanup(h.Close)

	if frame := h.Handle(ctx, &connectorv1.Command{CommandId: "c2", Type: "PING"}); frame != nil {
		t.Fatalf("PING should pass through (nil), got %+v", frame)
	}
}

// TestDBProbeSnapshotCommands spec-1.4：三类快照指令对真 PG 各产一帧 DataUpload，
// kind 与 command_id 正确回填；未装 pg_stat_statements 的 PG 上慢查询走能力降级
// （仍是成功路径的 DataUpload，不是 CommandResult 错误）。
func TestDBProbeSnapshotCommands(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	h, err := New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("new probe: %v", err)
	}
	t.Cleanup(h.Close)

	cases := []struct {
		command string
		kind    string
	}{
		{CommandProbeSlowlog, metrics.SnapshotKindSlowlog},
		{CommandProbeSchema, metrics.SnapshotKindSchema},
		{CommandProbeConfig, metrics.SnapshotKindConfig},
	}
	for _, tc := range cases {
		frame := h.Handle(ctx, &connectorv1.Command{
			CommandId: "cmd-" + tc.kind,
			Type:      tc.command,
			Payload:   []byte(`{"datasource_id":"ds1","engine_family":"postgres"}`),
		})
		upload := frame.GetDataUpload()
		if upload == nil {
			t.Fatalf("%s: expected a DataUpload frame, got %+v", tc.command, frame)
		}
		if upload.GetKind() != tc.kind || upload.GetCommandId() != "cmd-"+tc.kind {
			t.Fatalf("%s: frame header = kind %q id %q", tc.command, upload.GetKind(), upload.GetCommandId())
		}

		var snapshot metrics.Snapshot
		if err := json.Unmarshal(upload.GetPayload(), &snapshot); err != nil {
			t.Fatalf("%s: decode snapshot: %v", tc.command, err)
		}
		if snapshot.Kind != tc.kind || snapshot.DatasourceID != "ds1" {
			t.Fatalf("%s: snapshot envelope = %+v", tc.command, snapshot)
		}
		switch tc.kind {
		case metrics.SnapshotKindConfig:
			if len(snapshot.Configs) < 100 {
				t.Fatalf("config snapshot has only %d entries", len(snapshot.Configs))
			}
		case metrics.SnapshotKindSlowlog:
			// 测试容器无 pg_stat_statements → 结构化降级，非错误。
			if !snapshot.CapabilityMissing && len(snapshot.SlowQueries) == 0 {
				t.Fatalf("slowlog snapshot neither degraded nor populated: %+v", snapshot)
			}
		}
	}
}
