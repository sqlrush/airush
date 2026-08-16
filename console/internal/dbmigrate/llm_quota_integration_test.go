//go:build integration

package dbmigrate

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/testkit"
)

// TestLLMQuotaUsageMigration spec-1.7 T17：0005 up→down→up 幂等；两表 RLS 四要素；
// dev 租户预算 seed 就位；跨租户不可见；用量表对应用角色只增不改（审计语义）。
func TestLLMQuotaUsageMigration(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	db, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("first up: %v", err)
	}
	// 回滚到 0004（版本定位，不数步数——0002/0003/0004 三次教训）
	downTo(t, db, pg.ConnString, 4)
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables
		WHERE table_name IN ('llm_quotas','llm_usage')`); n != 0 {
		t.Fatalf("down 后 llm 表仍在: %d", n)
	}
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	// 四要素
	for _, tbl := range []string{"llm_quotas", "llm_usage"} {
		var enabled, forced bool
		if err := db.QueryRow(`SELECT relrowsecurity, relforcerowsecurity FROM pg_class
			WHERE relname = $1`, tbl).Scan(&enabled, &forced); err != nil {
			t.Fatalf("%s rls flags: %v", tbl, err)
		}
		if !enabled || !forced {
			t.Fatalf("%s: ENABLE=%v FORCE=%v", tbl, enabled, forced)
		}
		if n := countRows(t, db, `SELECT count(*) FROM pg_policies WHERE tablename = '`+tbl+`'
			AND policyname = 'tenant_isolation'`); n != 1 {
			t.Fatalf("%s: policy 缺失", tbl)
		}
	}

	// seed：dev 租户 5 千万 token 月度预算
	var budget int64
	var hard bool
	if err := db.QueryRow(`SELECT token_budget, hard_stop FROM llm_quotas
		WHERE tenant_id = $1 AND period = 'monthly'`, devTenantID).Scan(&budget, &hard); err != nil {
		t.Fatalf("seed 缺失: %v", err)
	}
	if budget != 50_000_000 || !hard {
		t.Fatalf("seed = (%d, %v), want (50000000, true)", budget, hard)
	}

	// 跨租户不可见 + 只增不改
	mustExec(t, db, `INSERT INTO tenants (id, name, slug) VALUES ('`+tenantBID+`', '租户B', 'tenant-b')
		ON CONFLICT DO NOTHING`)
	mustExec(t, db, `INSERT INTO llm_quotas (tenant_id, period, token_budget) VALUES ('`+tenantBID+`', 'monthly', 1)`)
	for _, tid := range []string{devTenantID, tenantBID} {
		mustExec(t, db, `INSERT INTO llm_usage (tenant_id, model, prompt_tokens, completion_tokens,
			total_tokens, status, idem_key) VALUES ('`+tid+`', 'chat-default', 1, 1, 2, 'ok', 'k-`+tid[:8]+`')`)
	}
	tx := beginAsApp(t, db, devTenantID)
	defer func() { _ = tx.Rollback() }()
	var quotas, usages int
	if err := tx.QueryRow(`SELECT (SELECT count(*) FROM llm_quotas), (SELECT count(*) FROM llm_usage)`).
		Scan(&quotas, &usages); err != nil {
		t.Fatalf("count as app: %v", err)
	}
	if quotas != 1 || usages != 1 {
		t.Fatalf("dev 租户视角 quotas=%d usages=%d, want 1/1（跨租户可见 = P0）", quotas, usages)
	}
	// 失败语句会把事务置为 aborted，用 savepoint 隔离（与 T10 同法）。
	txExec(t, tx, "SAVEPOINT before_update")
	if _, err := tx.Exec(`UPDATE llm_usage SET total_tokens = 999`); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("应用角色不该能改用量（审计语义）: %v", err)
	}
	txExec(t, tx, "ROLLBACK TO SAVEPOINT before_update")
}
