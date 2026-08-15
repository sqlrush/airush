//go:build integration

package tsstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// R1（spec-1.5 DoD 硬门槛）：写入必须经隔离视图，而不是图省事直插基表。
// 代价上限是 30%——超了就退回 §8 Q2 选项 B（独立 airush_ingest 角色 + 基表直授权），
// 那条路要多一个角色和一套授权，只有在性能真的过不去时才值得。
//
// 为什么值得单独一条用例：视图写入的开销来自 check_option 的每行求值，
// 行数一大就放大。等上线后才发现来不及——那时改的是隔离形态，不是一行代码。
const (
	r1Rows        = 20000
	r1Rounds      = 3
	r1MaxOverhead = 0.30
)

// TestViewWriteOverheadWithinBudget 交替跑「经视图」与「直插基表」各 r1Rounds 轮，
// 每条路径取最小耗时（吞吐类基准里最小值最稳，能滤掉容器/宿主机的偶发抖动）。
func TestViewWriteOverheadWithinBudget(t *testing.T) {
	store, ctx := fixture(t)
	rows := makeBenchRows(r1Rows)

	viewBest, baseBest := time.Duration(1<<62), time.Duration(1<<62)
	for i := 0; i < r1Rounds; i++ {
		// 交替执行：若宿主机在测试期间逐渐变忙，漂移会同等落在两条路径上。
		viewBest = minDur(viewBest, timeIt(t, func() { insertViaView(t, store, ctx, rows) }))
		baseBest = minDur(baseBest, timeIt(t, func() { insertViaBaseTable(t, store, rows) }))
	}

	overhead := float64(viewBest-baseBest) / float64(baseBest)
	t.Logf("R1 基准：视图 %v / 基表 %v / 退化 %.1f%%（%d 行 × %d 轮取最小）",
		viewBest, baseBest, overhead*100, r1Rows, r1Rounds)
	if overhead > r1MaxOverhead {
		t.Fatalf("经视图写入退化 %.1f%% > %.0f%%——按 spec-1.5 §8 Q2 应改用选项 B"+
			"（独立 airush_ingest 角色对基表直授权），并同步修订 spec",
			overhead*100, r1MaxOverhead*100)
	}
}

func makeBenchRows(n int) []seriesRow {
	base := time.Now().UTC().Add(-time.Hour)
	out := make([]seriesRow, n)
	for i := range out {
		out[i] = seriesRow{
			datasourceID: dsID,
			seriesName:   "db.connections.active",
			value:        float64(i % 100),
			at:           base.Add(time.Duration(i) * time.Millisecond),
		}
	}
	return out
}

// insertViaView 走生产写路径（inTenantTx + collected.series 视图）。
func insertViaView(t *testing.T, s *Store, ctx context.Context, rows []seriesRow) {
	t.Helper()
	if err := s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return s.insertSeries(ctx, tx, rows)
	}); err != nil {
		t.Fatalf("insert via view: %v", err)
	}
}

// insertViaBaseTable 是对照组：超级用户直插基表，无角色切换、无视图谓词。
// 批大小与视图路径一致，差异只剩"经不经视图"这一项。
func insertViaBaseTable(t *testing.T, s *Store, rows []seriesRow) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for start := 0; start < len(rows); start += s.batchMaxRows {
		end := min(start+s.batchMaxRows, len(rows))
		batch := &pgx.Batch{}
		for _, r := range rows[start:end] {
			batch.Queue(`INSERT INTO tsdb.series
				(tenant_id, datasource_id, series_name, entity_id, value, at)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				devTenantID, r.datasourceID, r.seriesName, r.entityID, r.value, r.at)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			t.Fatalf("insert via base table [%d,%d): %v", start, end, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func timeIt(t *testing.T, fn func()) time.Duration {
	t.Helper()
	start := time.Now()
	fn()
	return time.Since(start)
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
