//go:build integration

package tsstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
	"github.com/sqlrush/airush/testkit"
)

const (
	devTenantID = "00000000-0000-0000-0000-000000000001"
	otherTenant = "22222222-2222-2222-2222-222222222222"
	dsID        = "aaaaaaaa-0000-0000-0000-00000000000a"
	credID      = "cccccccc-0000-0000-0000-00000000000a"
)

// fixture 起容器、迁移、预置数据源，返回 Store 与 dev 租户 ctx。
func fixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	for _, q := range []string{
		`INSERT INTO datasource_credentials (tenant_id, id, username, secret_ciphertext, key_id)
		 VALUES ('` + devTenantID + `', '` + credID + `', 'u', '\x00'::bytea, 'k1')`,
		`INSERT INTO datasources (tenant_id, id, name, engine_family, connect_mode,
			credential_id, host, port)
		 VALUES ('` + devTenantID + `', '` + dsID + `', 'ds', 'postgres', 'direct',
			'` + credID + `', 'h', 5432)`,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	pool, err := pgxpool.New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return NewWithPool(pool, 0), tenancy.WithTenant(ctx, devTenantID)
}

// TestPublishMetricsRoundTrip spec-1.5 T1 + T17：指标批落库后经查询面读回，
// 且窗口在保留期内时命中原始层。
func TestPublishMetricsRoundTrip(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	batch := metrics.Batch{
		DatasourceID: dsID, EngineFamily: "postgres", CatalogVersion: metrics.CatalogVersion,
		CollectedAt: now,
		Metrics: []metrics.Metric{
			{Name: "db.connections.active", Value: 7, Unit: metrics.UnitCount, At: now.Add(-2 * time.Minute)},
			{Name: "db.connections.active", Value: 9, Unit: metrics.UnitCount, At: now.Add(-1 * time.Minute)},
			{Name: "db.cache.hit_ratio", Value: 0.97, Unit: metrics.UnitRatio, At: now.Add(-time.Minute)},
		},
	}
	if err := store.Publish(ctx, batch); err != nil {
		t.Fatalf("publish: %v", err)
	}

	points, err := store.SeriesRange(ctx, dsID, "db.connections.active", "",
		now.Add(-time.Hour), now.Add(time.Minute), time.Hour)
	if err != nil {
		t.Fatalf("series range: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("points = %d, want 1（1 小时桶应把两个点并成一个）", len(points))
	}
	p := points[0]
	if p.Avg != 8 || p.Min != 7 || p.Max != 9 || p.Samples != 2 {
		t.Fatalf("point = %+v, want avg=8 min=7 max=9 samples=2", p)
	}
}

// TestPublishRejectsUndeclaredSeries spec-1.5 T6：AD-3 防线在落库层生效——
// 未声明的 series 显式报错，不静默写进去。
func TestPublishRejectsUndeclaredSeries(t *testing.T) {
	store, ctx := fixture(t)
	err := store.Publish(ctx, metrics.Batch{
		DatasourceID: dsID,
		Metrics: []metrics.Metric{{
			Name: "db.totally.made_up", Value: 1, At: time.Now(),
		}},
	})
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTimeseriesUndeclaredSeries {
		t.Fatalf("err = %v, want AR_TIMESERIES_UNDECLARED_SERIES", err)
	}
}

// TestPublishRequiresTenantContext spec-1.5 §3.1：无租户上下文 fail-closed。
func TestPublishRequiresTenantContext(t *testing.T) {
	store, _ := fixture(t)
	err := store.Publish(context.Background(), metrics.Batch{
		DatasourceID: dsID,
		Metrics:      []metrics.Metric{{Name: "db.connections.active", Value: 1, At: time.Now()}},
	})
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTenantContextMissing {
		t.Fatalf("err = %v, want AR_TENANT_CONTEXT_MISSING", err)
	}
}

// TestSlowlogSnapshotExpansion spec-1.5 T2 + T18：慢查询快照展开成实体 + 5 条 series，
// TopEntities 能按累计耗时排出来并带上 SQL 文本。
func TestSlowlogSnapshotExpansion(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC().Truncate(time.Second)

	snap := metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindSlowlog,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, Source: "pg_stat_statements",
		SlowQueries: []metrics.SlowQueryEntry{
			{QueryID: "111", Text: "SELECT * FROM orders WHERE id = $1",
				Calls: 100, TotalMs: 5000, MeanMs: 50, MaxMs: 300, Rows: 100},
			{QueryID: "222", Text: "SELECT * FROM users WHERE id = $1",
				Calls: 10, TotalMs: 200, MeanMs: 20, MaxMs: 40, Rows: 10},
		},
	}
	if err := store.PublishSnapshot(ctx, snap); err != nil {
		t.Fatalf("publish snapshot: %v", err)
	}

	top, err := store.TopEntities(ctx, dsID, metrics.SeriesSlowlogTotalSec,
		now.Add(-time.Hour), now.Add(time.Minute), 10)
	if err != nil {
		t.Fatalf("top entities: %v", err)
	}
	if len(top) != 2 {
		t.Fatalf("top = %d entities, want 2", len(top))
	}
	if top[0].Label != "SELECT * FROM orders WHERE id = $1" {
		t.Fatalf("top[0].Label = %q，排序或字典 join 有问题", top[0].Label)
	}
	// 5000ms → 5s（规范层单位统一的换算点）
	if top[0].Total != 5 {
		t.Fatalf("top[0].Total = %v, want 5（毫秒应已换算为秒）", top[0].Total)
	}
	if top[0].NativeID != "111" {
		t.Fatalf("native_id = %q, want 111（引擎原生标识应留存供排障）", top[0].NativeID)
	}
	// 实体 ID 是内容哈希而非引擎给的 ID
	if top[0].EntityID == "111" {
		t.Fatal("entity_id 用了引擎原生 ID，跨实例不可比")
	}

	// 5 条 series 都落了
	for _, decl := range metrics.SlowlogSeries {
		pts, err := store.SeriesRange(ctx, dsID, decl.Name, top[0].EntityID,
			now.Add(-time.Hour), now.Add(time.Minute), time.Hour)
		if err != nil {
			t.Fatalf("%s range: %v", decl.Name, err)
		}
		if len(pts) != 1 {
			t.Fatalf("%s points = %d, want 1", decl.Name, len(pts))
		}
	}
}

// TestSnapshotHashDedup spec-1.5 T3/T4/T5：首次入库→当前版本；同内容不新增行；
// 异内容旧行封版、新行成为当前——变更历史成链。
func TestSnapshotHashDedup(t *testing.T) {
	store, ctx := fixture(t)
	base := time.Now().UTC().Truncate(time.Second)

	mk := func(at time.Time, tables ...metrics.TableInfo) metrics.Snapshot {
		return metrics.Snapshot{
			DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindSchema,
			CatalogVersion: metrics.CatalogVersion, CollectedAt: at, Source: "pg_catalog",
			Tables: tables,
		}
	}
	t1 := metrics.TableInfo{Schema: "public", Name: "orders", SizeBytes: 1024, RowEstimate: 10}

	// T3 首次
	if err := store.PublishSnapshot(ctx, mk(base, t1)); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	hist, _ := store.SnapshotHistory(ctx, dsID, metrics.SnapshotKindSchema, 10)
	if len(hist) != 1 {
		t.Fatalf("versions = %d, want 1", len(hist))
	}
	firstHash := hist[0].ContentHash

	// T4 同内容再来一次：不新增版本，只推进 collected_at
	later := base.Add(time.Hour)
	if err := store.PublishSnapshot(ctx, mk(later, t1)); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	hist, _ = store.SnapshotHistory(ctx, dsID, metrics.SnapshotKindSchema, 10)
	if len(hist) != 1 {
		t.Fatalf("同内容产生了 %d 个版本，哈希去重失效", len(hist))
	}
	if !hist[0].CollectedAt.Equal(later) {
		t.Fatalf("collected_at = %v, want %v（未变更也应推进观察时间）", hist[0].CollectedAt, later)
	}

	// T5 内容变了：旧版本封版，新版本成为当前
	changed := base.Add(2 * time.Hour)
	t2 := metrics.TableInfo{Schema: "public", Name: "orders", SizeBytes: 999999, RowEstimate: 5000}
	if err := store.PublishSnapshot(ctx, mk(changed, t2)); err != nil {
		t.Fatalf("third publish: %v", err)
	}
	hist, _ = store.SnapshotHistory(ctx, dsID, metrics.SnapshotKindSchema, 10)
	if len(hist) != 2 {
		t.Fatalf("versions = %d, want 2（变更应成链）", len(hist))
	}
	if hist[0].SupersededAt != nil {
		t.Fatal("最新版本不应有 superseded_at")
	}
	if hist[1].SupersededAt == nil || hist[1].ContentHash != firstHash {
		t.Fatalf("旧版本未正确封版: %+v", hist[1])
	}

	// 当前版本内容是新的
	latest, err := store.LatestSnapshot(ctx, dsID, metrics.SnapshotKindSchema)
	if err != nil || latest == nil {
		t.Fatalf("latest: %v / %v", latest, err)
	}
	if len(latest.Tables) != 1 || latest.Tables[0].SizeBytes != 999999 {
		t.Fatalf("latest payload = %+v", latest.Tables)
	}
}

// TestCapabilityMissingSnapshotStored spec-1.5 T11：能力缺失是成功路径的结构化降级，
// 照常入库供上层提示"请开启 pg_stat_statements"，不是错误也不是丢弃。
func TestCapabilityMissingSnapshotStored(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC()

	err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindConfig,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, CapabilityMissing: true,
	})
	if err != nil {
		t.Fatalf("publish capability-missing snapshot: %v", err)
	}
	latest, err := store.LatestSnapshot(ctx, dsID, metrics.SnapshotKindConfig)
	if err != nil || latest == nil {
		t.Fatalf("latest: %v / %v", latest, err)
	}
	if !latest.CapabilityMissing {
		t.Fatal("capability_missing 未落库，上层无从提示开启采集能力")
	}
}

// TestUnsupportedSnapshotKindRejected spec-1.5 §3：规则 6——未支持分支显式报错。
func TestUnsupportedSnapshotKindRejected(t *testing.T) {
	store, ctx := fixture(t)
	err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, Kind: "bogus", CollectedAt: time.Now(),
	})
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeCollectUnsupportedKind {
		t.Fatalf("err = %v, want AR_COLLECT_UNSUPPORTED_KIND", err)
	}
}

// TestCrossTenantInvisible spec-1.5：另一租户写入的读数在本租户查询面完全不可见
// （T7 在迁移层验过视图谓词，这里验的是经 tsstore 全链路后依然成立）。
func TestCrossTenantInvisible(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC()

	// 造第二个租户及其数据源与读数
	otherCtx := tenancy.WithTenant(context.Background(), otherTenant)
	seedOtherTenant(t, store, otherTenant)
	if err := store.Publish(otherCtx, metrics.Batch{
		DatasourceID: otherDS, Metrics: []metrics.Metric{
			{Name: "db.connections.active", Value: 999, At: now.Add(-time.Minute)},
		},
	}); err != nil {
		t.Fatalf("publish as other tenant: %v", err)
	}

	// dev 租户查另一租户的数据源 ID —— 必须查不到任何点
	pts, err := store.SeriesRange(ctx, otherDS, "db.connections.active", "",
		now.Add(-time.Hour), now.Add(time.Minute), time.Hour)
	if err != nil {
		t.Fatalf("series range: %v", err)
	}
	if len(pts) != 0 {
		t.Fatalf("跨租户可见 %d 个点，want 0（P0）", len(pts))
	}
}

const (
	otherDS     = "bbbbbbbb-0000-0000-0000-00000000000b"
	otherCredID = "dddddddd-0000-0000-0000-00000000000d"
)

func seedOtherTenant(t *testing.T, store *Store, tenantID string) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name, slug) VALUES ($1, '租户B', 'tenant-b')
		  ON CONFLICT DO NOTHING`, []any{tenantID}},
		{`INSERT INTO datasource_credentials (tenant_id, id, username, secret_ciphertext, key_id)
		  VALUES ($1, $2, 'u', '\x00'::bytea, 'k1')`, []any{tenantID, otherCredID}},
		{`INSERT INTO datasources (tenant_id, id, name, engine_family, connect_mode,
			credential_id, host, port)
		  VALUES ($1, $2, 'ds-b', 'postgres', 'direct', $3, 'h', 5432)`,
			[]any{tenantID, otherDS, otherCredID}},
	} {
		if _, err := store.pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed other tenant: %v", err)
		}
	}
}
