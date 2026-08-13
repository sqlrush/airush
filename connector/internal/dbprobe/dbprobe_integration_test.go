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
		if m.Name == "pg.cache.hit_ratio" && (m.Value < 0 || m.Value > 1) {
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
