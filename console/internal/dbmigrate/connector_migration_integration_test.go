//go:build integration

package dbmigrate

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/testkit"
)

// TestConnectorEnrollmentMigration spec-1.2 D2：0003 六值状态机 + 令牌列的
// up/down/up 与约束语义（user 评审 2026-08-11 的 DDL 定版实证）。
func TestConnectorEnrollmentMigration(t *testing.T) {
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
		t.Fatalf("up: %v", err)
	}

	// 默认值 pending + 六值域生效
	mustExec(t, db, `INSERT INTO connectors (tenant_id, id, name) VALUES
		('`+devTenantID+`', '66666666-6666-6666-6666-666666666666', 'mig-conn')`)
	var status string
	if err := db.QueryRow(`SELECT status FROM connectors
		WHERE id = '66666666-6666-6666-6666-666666666666'`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("default status = %q (err=%v), want pending", status, err)
	}
	mustExec(t, db, `UPDATE connectors SET status = 'revoked', revoked_at = now(),
		enroll_token_hash = 'abc', enroll_token_expires_at = now()
		WHERE id = '66666666-6666-6666-6666-666666666666'`)
	if _, err := db.Exec(`UPDATE connectors SET status = 'bogus'
		WHERE id = '66666666-6666-6666-6666-666666666666'`); err == nil {
		t.Fatal("bogus status accepted, want CHECK violation")
	}

	// down 1 步：列消失、超集值降级 offline
	if err := RunWithURL(pg.ConnString, []string{"down"}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'connectors' AND column_name = 'enroll_token_hash'`); n != 0 {
		t.Fatal("enroll_token_hash still present after down")
	}
	if err := db.QueryRow(`SELECT status FROM connectors
		WHERE id = '66666666-6666-6666-6666-666666666666'`).Scan(&status); err != nil || status != "offline" {
		t.Fatalf("post-down status = %q (err=%v), want offline (coerced)", status, err)
	}

	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("re-up: %v", err)
	}
}
