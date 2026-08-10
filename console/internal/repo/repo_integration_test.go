//go:build integration

package repo

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/testkit"
)

const (
	devTenantID = "00000000-0000-0000-0000-000000000001"
	tenantBID   = "22222222-2222-2222-2222-222222222222"
)

// TestInTenantTxRLS spec-1.1 T2（repo 面）：经基座写入的行只在本租户事务可见；
// 换租户上下文即 0 行——RLS 应用层执行路径（SET LOCAL ROLE + GUC）实证。
func TestInTenantTxRLS(t *testing.T) {
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	// 租户 B 主档以超级用户预置（租户管理不在本 spec 范围）
	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.Exec(`INSERT INTO tenants (id, name, slug) VALUES
		($1, '租户B', 'tenant-b')`, tenantBID); err != nil {
		t.Fatalf("seed tenant b: %v", err)
	}

	store, err := New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	devCtx := tenancy.WithTenant(ctx, devTenantID)
	bCtx := tenancy.WithTenant(ctx, tenantBID)

	// dev 租户经基座写 agent
	err = store.InTenantTx(devCtx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO agents (tenant_id, name, kind)
			VALUES ($1, 'a1', 'assistant')`, devTenantID)
		return err
	})
	if err != nil {
		t.Fatalf("insert agent as dev: %v", err)
	}

	if got := countAgents(t, store, devCtx); got != 1 {
		t.Fatalf("dev tenant sees %d agents, want 1", got)
	}
	if got := countAgents(t, store, bCtx); got != 0 {
		t.Fatalf("tenant B sees %d agents, want 0 (RLS breach!)", got)
	}

	// 租户 B 冒充写入 dev 租户行 → RLS WITH CHECK 拒绝
	err = store.InTenantTx(bCtx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO agents (tenant_id, name, kind)
			VALUES ($1, 'evil', 'assistant')`, devTenantID)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert accepted, want RLS violation")
	}
}

func countAgents(t *testing.T, store *Store, ctx context.Context) int {
	t.Helper()
	var n int
	err := store.InTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM agents`).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count agents: %v", err)
	}
	return n
}
