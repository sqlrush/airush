//go:build integration

// spec-1.3 Direct 通道端到端：真实 PG → directconn.MetricsQuerier → Probe.Collect →
// BufferSink。覆盖 T2（指标合理域）/T8（Sink 收讫）/Direct Querier 适配。
package directconn_test

import (
	"context"
	"testing"

	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/libs/metrics"
)

func TestDirectMetricsCollection(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	q := e.mgr.MetricsQuerier(id)
	probe := metrics.Probe{DatasourceID: id, EngineFamily: "postgres"}
	batch, err := probe.Collect(e.tenant, q)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	// T8：Sink 收讫
	sink := metrics.NewBufferSink(16)
	if err := sink.Publish(context.Background(), batch); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if sink.Total() != 1 {
		t.Fatalf("sink total = %d, want 1", sink.Total())
	}

	// T2：指标值合理域
	byName := map[string]metrics.Metric{}
	for _, m := range batch.Metrics {
		byName[m.Name] = m
	}
	if m, ok := byName["db.connections.total"]; !ok || m.Value < 1 {
		t.Fatalf("connections.total = %+v (want >=1)", m)
	}
	if m, ok := byName["db.cache.hit_ratio"]; ok {
		if m.Value < 0 || m.Value > 1 {
			t.Fatalf("cache.hit_ratio = %v, want 0..1", m.Value)
		}
	}
	if m, ok := byName["db.connections.max"]; !ok || m.Value < 1 {
		t.Fatalf("connections.max = %+v", m)
	}
	// 复制延迟在单机主库应缺采（partial），不算错误
	if _, ok := byName["pg.replication.lag_bytes"]; ok {
		t.Log("replication lag present (unexpected on standalone but allowed)")
	}
	// datasource_id label 白名单
	for _, m := range batch.Metrics {
		if m.Labels["datasource_id"] != id {
			t.Fatalf("metric %s missing datasource_id label", m.Name)
		}
	}
}
