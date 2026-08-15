//go:build integration

package tsstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// 本文件覆盖 spec-1.5 里"存储引擎行为"那一组门槛（T13/T15/T16/T17/T19/T20）。
// 它们与 tsstore_integration_test.go 的读写语义用例分开：那边验的是我们写的转换逻辑，
// 这边验的是 TimescaleDB 的连续聚合、保留、级联在**我们的配置下**确实是那样——
// 这类假设错了不会报错，只会让曲线悄悄缺一段。

const poisonValue = -424242

// adminExec 以超级用户身份执行（跳过租户视图，用于造数据与调用 TimescaleDB 管理函数）。
// 一律不带参数：pgx 对无参 Exec 走简单协议，而 refresh_continuous_aggregate / drop_chunks
// 不能在事务块里跑，扩展协议的隐式事务会踩到这一点。
func adminExec(t *testing.T, s *Store, sql string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), sql); err != nil {
		t.Fatalf("admin exec %.70s...: %v", sql, err)
	}
}

func adminCount(t *testing.T, s *Store, sql string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatalf("admin count %.70s...: %v", sql, err)
	}
	return n
}

// tsLiteral 把时间写成 SQL 字面量（本文件所有 SQL 都是测试自造，无外部输入）。
func tsLiteral(at time.Time) string {
	return "'" + at.UTC().Format(time.RFC3339Nano) + "'::timestamptz"
}

// seedRawPoint 直接写基表：造历史数据要能指定任意时刻，而写路径只接受目录声明的
// series，两者关注点不同，这里刻意绕过写路径。
func seedRawPoint(t *testing.T, s *Store, tenantID, datasourceID, name, entityID string,
	value float64, at time.Time) {
	t.Helper()
	adminExec(t, s, fmt.Sprintf(`INSERT INTO tsdb.series
		(tenant_id, datasource_id, series_name, entity_id, value, at)
		VALUES ('%s', '%s', '%s', '%s', %v, %s)`,
		tenantID, datasourceID, name, entityID, value, tsLiteral(at)))
}

func refreshCAgg(t *testing.T, s *Store, view string, from, to time.Time) {
	t.Helper()
	adminExec(t, s, fmt.Sprintf(`CALL refresh_continuous_aggregate('%s', %s, %s)`,
		view, tsLiteral(from), tsLiteral(to)))
}

// oneBucket 经**应用视图**读某个时间范围内的单个桶（不是直读 tsdb，也不走 SeriesRange：
// 这里要验的是聚合视图本身的数值，不该被选层逻辑搅进来）。
func oneBucket(t *testing.T, s *Store, ctx context.Context, relation, name string,
	from, to time.Time) Point {
	t.Helper()
	// #nosec G201 —— relation 是本文件的常量，无外部输入。
	sql := fmt.Sprintf(`SELECT bucket, avg_value, min_value, max_value, last_value, sample_count
		  FROM %s
		 WHERE datasource_id = $1 AND series_name = $2 AND entity_id = ''
		   AND bucket >= $3 AND bucket < $4`, relation)

	var p Point
	err := s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, dsID, name, from, to).
			Scan(&p.At, &p.Avg, &p.Min, &p.Max, &p.Last, &p.Samples)
	})
	if err != nil {
		t.Fatalf("read %s [%v, %v): %v", relation, from, to, err)
	}
	return p
}

// closeEnough 比较浮点聚合结果（连续聚合内部按 double 累加，不能要求逐位相等）。
func closeEnough(got, want float64) bool {
	d := got - want
	return d < 1e-9 && d > -1e-9
}

// TestBatchSplitting spec-1.5 T13：行数超 batchMaxRows 时分批下发，
// 但整批仍在同一事务里——中途失败必须零残留，否则下游会按半份数据算出错误趋势。
func TestBatchSplitting(t *testing.T) {
	store, ctx := fixture(t)
	small := NewWithPool(store.pool, 2) // 每批 2 行，7 行 → 4 批
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	mk := func(poison bool) metrics.Batch {
		b := metrics.Batch{DatasourceID: dsID, CollectedAt: base}
		for i := 0; i < 7; i++ {
			v := float64(i + 1)
			if poison && i == 4 { // 第 3 批的第 1 行
				v = poisonValue
			}
			b.Metrics = append(b.Metrics, metrics.Metric{
				Name: "db.connections.active", Value: v,
				At: base.Add(time.Duration(i) * time.Second),
			})
		}
		return b
	}

	if err := small.Publish(ctx, mk(false)); err != nil {
		t.Fatalf("publish 7 rows with batchMaxRows=2: %v", err)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM tsdb.series`); n != 7 {
		t.Fatalf("落库 %d 行，want 7——分批把行丢了", n)
	}

	// 用一条测试期约束制造"第 3 批失败"。约束本身不是产品行为，只是失败注入手段。
	adminExec(t, store, fmt.Sprintf(
		`ALTER TABLE tsdb.series ADD CONSTRAINT test_poison CHECK (value <> %v)`, poisonValue))
	t.Cleanup(func() {
		adminExec(t, store, `ALTER TABLE tsdb.series DROP CONSTRAINT test_poison`)
	})

	err := small.Publish(ctx, mk(true))
	if err == nil {
		t.Fatal("中途约束冲突未报错——写失败被吞了")
	}
	// 顺带固化 AR_TIMESERIES_WRITE_FAILED 的触发用例（规则 4：每个错误码有触发用例）。
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTimeseriesWriteFailed {
		t.Fatalf("err = %v, want AR_TIMESERIES_WRITE_FAILED", err)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM tsdb.series`); n != 7 {
		t.Fatalf("失败后共 %d 行，want 仍是 7——前几批没回滚，分批破坏了事务语义", n)
	}
}

// TestSeriesRangeRejectsBadStep 是 AR_TIMESERIES_QUERY_FAILED 的触发用例。
// 非正 step 会让 time_bucket 报错，与其把库的报错原样抛出去，不如在入口显式拒绝。
func TestSeriesRangeRejectsBadStep(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC()
	_, err := store.SeriesRange(ctx, dsID, "db.connections.active", "",
		now.Add(-time.Hour), now, 0)
	var ae *apierror.Error
	if !errors.As(err, &ae) || ae.Code != apierror.CodeTimeseriesQueryFailed {
		t.Fatalf("err = %v, want AR_TIMESERIES_QUERY_FAILED", err)
	}
}

// TestContinuousAggregateConsistency spec-1.5 T15 + T16：
// T15 物化后的 5m/1h 数值与原始点直接聚合一致（含 1h 层的加权平均）；
// T16 real-time aggregation 打开——水位线之后刚写入、尚未物化的点照样查得到，
// 否则控制台会出现"最近几分钟没数据"的假缺口。
func TestContinuousAggregateConsistency(t *testing.T) {
	store, ctx := fixture(t)
	const name = "db.connections.active"

	// 基准时刻取整点：两个 5 分钟桶必须落在同一小时内，1h 层的比对才有意义。
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	// 桶 1：1,2,3,4,5 → avg 3 min 1 max 5 count 5
	for i := 1; i <= 5; i++ {
		seedRawPoint(t, store, devTenantID, dsID, name, "", float64(i),
			hour.Add(time.Duration(i-1)*time.Minute))
	}
	// 桶 2：10,20 → avg 15 min 10 max 20 count 2
	seedRawPoint(t, store, devTenantID, dsID, name, "", 10, hour.Add(5*time.Minute))
	seedRawPoint(t, store, devTenantID, dsID, name, "", 20, hour.Add(6*time.Minute))

	// T16 上半：一次都没刷新，桶数据就应该能查到（实时聚合部分）。
	if got := oneBucket(t, store, ctx, "collected.series_5m", name,
		hour, hour.Add(5*time.Minute)); got.Samples != 5 {
		t.Fatalf("未物化时 5m 视图 samples=%d，want 5——real-time aggregation 没打开，"+
			"materialized_only 在 2.13+ 默认 true，那样刚采到的数据看不见", got.Samples)
	}

	// 物化到 hour+10min 为止：水位线之后的写入仍走实时部分，T16 下半靠这个边界。
	refreshCAgg(t, store, "tsdb.series_5m", hour.Add(-time.Hour), hour.Add(10*time.Minute))
	refreshCAgg(t, store, "tsdb.series_1h", hour.Add(-2*time.Hour), hour.Add(time.Hour))

	// T15：物化后的值与原始点聚合一致
	b1 := oneBucket(t, store, ctx, "collected.series_5m", name, hour, hour.Add(5*time.Minute))
	if b1.Avg != 3 || b1.Min != 1 || b1.Max != 5 || b1.Last != 5 || b1.Samples != 5 {
		t.Fatalf("5m 桶1 = %+v, want avg=3 min=1 max=5 last=5 samples=5", b1)
	}
	b2 := oneBucket(t, store, ctx, "collected.series_5m", name,
		hour.Add(5*time.Minute), hour.Add(10*time.Minute))
	if b2.Avg != 15 || b2.Min != 10 || b2.Max != 20 || b2.Samples != 2 {
		t.Fatalf("5m 桶2 = %+v, want avg=15 min=10 max=20 samples=2", b2)
	}

	// 1h 层是 5m 的再聚合：(3*5 + 15*2)/7 = 45/7 ≈ 6.43。若退化成"平均的平均"会得 9，
	// 样本数不均时那个数是错的——这条断言就是防它。
	h := oneBucket(t, store, ctx, "collected.series_1h", name, hour, hour.Add(time.Hour))
	if want := 45.0 / 7.0; !closeEnough(h.Avg, want) {
		t.Fatalf("1h avg = %v, want %v（加权平均；得到 9 说明退化成了平均的平均）", h.Avg, want)
	}
	if h.Min != 1 || h.Max != 20 || h.Samples != 7 {
		t.Fatalf("1h 桶 = %+v, want min=1 max=20 samples=7", h)
	}

	// T16 下半：水位线之后再写入，未刷新也应立刻可见。
	seedRawPoint(t, store, devTenantID, dsID, name, "", 99, hour.Add(20*time.Minute))
	if got := oneBucket(t, store, ctx, "collected.series_5m", name,
		hour.Add(20*time.Minute), hour.Add(25*time.Minute)); got.Samples != 1 {
		t.Fatalf("水位线之后新写入的点在 5m 视图不可见（samples=%d）——实时聚合失效", got.Samples)
	}
}

// TestLayerSelectionSurvivesRetention spec-1.5 T17 + T19。
//
// 两条门槛合验，因为只有把原始 chunk 真删掉，才能证明长窗口查询确实走了聚合层：
// 若选层写错（长窗口仍读原始层），保留期一到，"最近半年"的图就会从某天起变成空白——
// 那种故障不会报错，只会被当成"没采到"。
func TestLayerSelectionSurvivesRetention(t *testing.T) {
	store, ctx := fixture(t)
	const name = "db.connections.active"
	now := time.Now().UTC()

	recent := now.Add(-2 * time.Hour)     // 原始层
	mid := now.Add(-30 * 24 * time.Hour)  // 5m 层
	old := now.Add(-200 * 24 * time.Hour) // 1h 层
	for _, p := range []struct {
		at time.Time
		v  float64
	}{{recent, 7}, {mid, 30}, {old, 200}} {
		seedRawPoint(t, store, devTenantID, dsID, name, "", p.v, p.at)
	}

	// 造一份实体与快照，用于验证 drop_chunks 只动读数流水（T19）。
	if err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindSchema,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, Source: "pg_catalog",
		Tables: []metrics.TableInfo{{Schema: "public", Name: "orders", SizeBytes: 1}},
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindSlowlog,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, Source: "pg_stat_statements",
		SlowQueries: []metrics.SlowQueryEntry{
			{QueryID: "1", Text: "SELECT 1", Calls: 1, TotalMs: 10, MeanMs: 10, MaxMs: 10, Rows: 1},
		},
	}); err != nil {
		t.Fatalf("seed slowlog: %v", err)
	}

	// 物化两层，然后按生产保留期删原始 chunk。
	refreshCAgg(t, store, "tsdb.series_5m", old.Add(-time.Hour), now)
	refreshCAgg(t, store, "tsdb.series_1h", old.Add(-2*time.Hour), now.Add(-time.Hour))
	adminExec(t, store, `SELECT drop_chunks('tsdb.series', older_than => INTERVAL '14 days')`)

	if n := adminCount(t, store, fmt.Sprintf(
		`SELECT count(*) FROM tsdb.series WHERE at < %s`, tsLiteral(now.Add(-15*24*time.Hour)))); n != 0 {
		t.Fatalf("drop_chunks 后仍有 %d 行超期原始数据", n)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM tsdb.series`); n == 0 {
		t.Fatal("drop_chunks 把保留期内的数据也删了")
	}

	// T19：实体与快照不受读数保留期影响——"这条慢 SQL 半年前就有了"必须仍答得出。
	if n := adminCount(t, store, `SELECT count(*) FROM collected.entities`); n != 1 {
		t.Fatalf("drop_chunks 后 entities = %d, want 1", n)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM collected.snapshots`); n != 1 {
		t.Fatalf("drop_chunks 后 snapshots = %d, want 1", n)
	}

	// T17：三档窗口各自选层，且都能拿到该层应有的数据。
	for _, c := range []struct {
		name  string
		from  time.Time
		want  float64
		layer string
	}{
		{"原始层", now.Add(-3 * time.Hour), 7, "collected.series"},
		{"5m 层", now.Add(-31 * 24 * time.Hour), 30, "collected.series_5m"},
		{"1h 层", now.Add(-201 * 24 * time.Hour), 200, "collected.series_1h"},
	} {
		if got := layerFor(c.from, now).relation; got != c.layer {
			t.Fatalf("%s：layerFor 选了 %s，want %s", c.name, got, c.layer)
		}
		pts, err := store.SeriesRange(ctx, dsID, name, "", c.from, now, 24*time.Hour)
		if err != nil {
			t.Fatalf("%s：SeriesRange: %v", c.name, err)
		}
		if !containsMax(pts, c.want) {
			t.Fatalf("%s：窗口内查不到值 %v（共 %d 个点）——原始 chunk 已删，"+
				"说明该窗口没走到聚合层", c.name, c.want, len(pts))
		}
	}
}

func containsMax(pts []Point, want float64) bool {
	for _, p := range pts {
		if p.Max == want {
			return true
		}
	}
	return false
}

// TestDatasourceDeleteCascade spec-1.5 T20：删数据源级联清理 entities/snapshots；
// 读数流水**不**级联（超表无 FK，靠保留期自然清理），这个差异是有意的，
// 一并断言住，免得日后有人"顺手补个 FK"把写入拖慢。
func TestDatasourceDeleteCascade(t *testing.T) {
	store, ctx := fixture(t)
	now := time.Now().UTC()

	if err := store.Publish(ctx, metrics.Batch{
		DatasourceID: dsID, CollectedAt: now,
		Metrics: []metrics.Metric{{Name: "db.connections.active", Value: 1, At: now}},
	}); err != nil {
		t.Fatalf("seed metrics: %v", err)
	}
	if err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindSlowlog,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, Source: "pg_stat_statements",
		SlowQueries: []metrics.SlowQueryEntry{
			{QueryID: "1", Text: "SELECT 1", Calls: 1, TotalMs: 10, MeanMs: 10, MaxMs: 10, Rows: 1},
		},
	}); err != nil {
		t.Fatalf("seed slowlog: %v", err)
	}
	if err := store.PublishSnapshot(ctx, metrics.Snapshot{
		DatasourceID: dsID, EngineFamily: "postgres", Kind: metrics.SnapshotKindConfig,
		CatalogVersion: metrics.CatalogVersion, CollectedAt: now, Source: "pg_settings",
		Configs: []metrics.ConfigEntry{{Name: "work_mem", Value: "4MB"}},
	}); err != nil {
		t.Fatalf("seed config snapshot: %v", err)
	}

	before := adminCount(t, store, `SELECT count(*) FROM tsdb.series`)
	if before == 0 {
		t.Fatal("前置数据没落库，本用例验不到级联")
	}

	adminExec(t, store, `DELETE FROM datasources WHERE id = '`+dsID+`'`)

	if n := adminCount(t, store, `SELECT count(*) FROM collected.entities`); n != 0 {
		t.Fatalf("删数据源后 entities 残留 %d 行——FK 级联缺失", n)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM collected.snapshots`); n != 0 {
		t.Fatalf("删数据源后 snapshots 残留 %d 行——FK 级联缺失", n)
	}
	if n := adminCount(t, store, `SELECT count(*) FROM tsdb.series`); n != before {
		t.Fatalf("读数流水被级联删了（%d → %d）。超表刻意不挂 FK："+
			"压缩下的级联删除代价过高，孤儿行由 14 天保留期清理（spec-1.5 §3.9）", before, n)
	}
}
