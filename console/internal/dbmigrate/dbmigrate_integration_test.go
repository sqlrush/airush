//go:build integration

package dbmigrate

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/testkit"
)

// TestMigrateLifecycleAndRLS spec-0.6 D5：单容器串行验证
// T1 up 就位 / T2 up-down-up 幂等 / T3 已最新零操作 / T4 租户隔离 /
// T5 fail-closed / T6 FORCE 拦 owner。
func TestMigrateLifecycleAndRLS(t *testing.T) {
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

	// T1：up → tenants 存在，version=1
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'tenants'`); n != 1 {
		t.Fatalf("tenants table not found after up")
	}

	// T3：已最新再 up → 零操作成功
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("idempotent up: %v", err)
	}

	// T2：down = 回滚 1 步——先退 0002（tenants 仍在），再退 0001（tenants 消失）；up 全量回归
	if err := RunWithURL(pg.ConnString, []string{"down"}); err != nil {
		t.Fatalf("down 0002: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'tenants'`); n != 1 {
		t.Fatalf("tenants table gone after single-step down (want it kept)")
	}
	if err := RunWithURL(pg.ConnString, []string{"down"}); err != nil {
		t.Fatalf("down 0001: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'tenants'`); n != 0 {
		t.Fatalf("tenants table still present after full down")
	}
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	verifyRLSTemplate(t, db)
}

// verifyRLSTemplate 按 spec-0.6 §2.2 模板建演示表，验证 T4/T5/T6 语义。
func verifyRLSTemplate(t *testing.T, db *sql.DB) {
	t.Helper()

	mustExec(t, db, `INSERT INTO tenants (id, name, slug) VALUES
		('11111111-1111-1111-1111-111111111111', '租户A', 'tenant-a'),
		('22222222-2222-2222-2222-222222222222', '租户B', 'tenant-b')`)

	// 模板四要素 + 非超级用户 owner（超级用户天然绕过 RLS，无法用于 T6）
	mustExec(t, db, `CREATE ROLE demo_owner NOLOGIN`)
	mustExec(t, db, `CREATE TABLE rls_demo (
		tenant_id uuid NOT NULL REFERENCES tenants(id),
		id        uuid NOT NULL DEFAULT gen_random_uuid(),
		v         text,
		PRIMARY KEY (tenant_id, id))`)
	mustExec(t, db, `ALTER TABLE rls_demo ENABLE ROW LEVEL SECURITY`)
	mustExec(t, db, `ALTER TABLE rls_demo FORCE ROW LEVEL SECURITY`)
	mustExec(t, db, `CREATE POLICY tenant_isolation ON rls_demo
		USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)`)
	mustExec(t, db, `GRANT SELECT, INSERT, UPDATE, DELETE ON rls_demo TO airush_app`)
	mustExec(t, db, `ALTER TABLE rls_demo OWNER TO demo_owner`)

	// 种子数据以超级用户写入（绕过 RLS 是预期）
	mustExec(t, db, `INSERT INTO rls_demo (tenant_id, v) VALUES
		('11111111-1111-1111-1111-111111111111', 'from-a'),
		('22222222-2222-2222-2222-222222222222', 'from-b')`)

	// T4：airush_app + SET LOCAL 只见本租户
	inTx(t, db, func(tx *sql.Tx) {
		txExec(t, tx, `SET ROLE airush_app`)
		txExec(t, tx, `SET LOCAL app.tenant_id = '11111111-1111-1111-1111-111111111111'`)
		if got := txCount(t, tx, `SELECT count(*) FROM rls_demo`); got != 1 {
			t.Fatalf("tenant A sees %d rows, want 1", got)
		}
		var v string
		if err := tx.QueryRow(`SELECT v FROM rls_demo`).Scan(&v); err != nil || v != "from-a" {
			t.Fatalf("tenant A sees %q (err=%v), want from-a", v, err)
		}
	})

	// T5：未设置变量 → 0 行（fail-closed，且不报错）
	inTx(t, db, func(tx *sql.Tx) {
		txExec(t, tx, `SET ROLE airush_app`)
		if got := txCount(t, tx, `SELECT count(*) FROM rls_demo`); got != 0 {
			t.Fatalf("no-var session sees %d rows, want 0 (fail-closed)", got)
		}
	})

	// T6：FORCE 拦非超级用户 owner
	inTx(t, db, func(tx *sql.Tx) {
		txExec(t, tx, `SET ROLE demo_owner`)
		if got := txCount(t, tx, `SELECT count(*) FROM rls_demo`); got != 0 {
			t.Fatalf("owner sees %d rows, want 0 (FORCE RLS)", got)
		}
	})
}

func inTx(t *testing.T, db *sql.DB, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	fn(tx)
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %.60s...: %v", q, err)
	}
}

func txExec(t *testing.T, tx *sql.Tx, q string) {
	t.Helper()
	if _, err := tx.Exec(q); err != nil {
		t.Fatalf("tx exec %.60s...: %v", q, err)
	}
}

func txCount(t *testing.T, tx *sql.Tx, q string) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("tx count %.60s...: %v", q, err)
	}
	return n
}

func countRows(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("count %.60s...: %v", q, err)
	}
	return n
}
