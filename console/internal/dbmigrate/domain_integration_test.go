//go:build integration

package dbmigrate

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sqlrush/airush/testkit"
)

// domainTables 是 0002_domain_model 交付的全部关系（spec-1.1 §2.1）。
var domainTables = []string{
	"users", "connectors", "datasource_groups", "agents",
	"datasource_credentials", "datasources", "datasource_aliases", "idempotency_keys",
}

const (
	devTenantID = "00000000-0000-0000-0000-000000000001"
	tenantBID   = "22222222-2222-2222-2222-222222222222"
)

// TestDomainModelMigration spec-1.1 T1：0002 up-down-up 幂等、八表就位、
// 模板四要素全覆盖、seed 落库、表级 CHECK 与跨租户复合外键生效。
func TestDomainModelMigration(t *testing.T) {
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

	// up → down → up 幂等（down 后领域表全部消失）
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := RunWithURL(pg.ConnString, []string{"down"}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables
		WHERE table_name = ANY('{users,datasources,agents}')`); n != 0 {
		t.Fatalf("domain tables still present after down: %d", n)
	}
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	verifyDomainTables(t, db)
	verifySeed(t, db)
	verifyModeChecks(t, db)
	verifyCrossTenantFK(t, db)
}

// verifyDomainTables 八表就位且 RLS 模板四要素（ENABLE+FORCE+policy）齐备。
func verifyDomainTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, tbl := range domainTables {
		var enabled, forced bool
		err := db.QueryRow(`SELECT relrowsecurity, relforcerowsecurity
			FROM pg_class WHERE relname = $1`, tbl).Scan(&enabled, &forced)
		if err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
		if !enabled || !forced {
			t.Fatalf("table %s: RLS enable=%v force=%v, want true/true", tbl, enabled, forced)
		}
		if n := countRows(t, db, `SELECT count(*) FROM pg_policies
			WHERE tablename = '`+tbl+`' AND policyname = 'tenant_isolation'`); n != 1 {
			t.Fatalf("table %s: tenant_isolation policy missing", tbl)
		}
	}
}

// verifySeed dev 租户与 dev 用户 seed 落库（spec-1.1 §8 Q5）。
func verifySeed(t *testing.T, db *sql.DB) {
	t.Helper()
	if n := countRows(t, db, `SELECT count(*) FROM tenants
		WHERE id = '`+devTenantID+`' AND slug = 'dev'`); n != 1 {
		t.Fatalf("dev tenant seed missing")
	}
	if n := countRows(t, db, `SELECT count(*) FROM users
		WHERE tenant_id = '`+devTenantID+`' AND role = 'admin'`); n != 1 {
		t.Fatalf("dev user seed missing")
	}
}

// verifyModeChecks 表级 CHECK：connect_mode 与 connector/credential 组合、group 配对。
func verifyModeChecks(t *testing.T, db *sql.DB) {
	t.Helper()

	// 前置：dev 租户先有一个 connector（组配对用例需要合法的 connector 模式载体）
	mustExec(t, db, `INSERT INTO connectors (tenant_id, id, name) VALUES
		('`+devTenantID+`', '55555555-5555-5555-5555-555555555555', 'conn-dev')`)

	// direct 模式带 connector_id → 拒绝
	_, err := db.Exec(`INSERT INTO datasources
		(tenant_id, name, engine_family, connect_mode, connector_id, host, port)
		VALUES ($1, 'bad-direct', 'postgres', 'direct',
		        '33333333-3333-3333-3333-333333333333', 'h', 5432)`, devTenantID)
	if err == nil {
		t.Fatalf("direct mode with connector_id accepted, want CHECK violation")
	}

	// connector 模式缺 connector_id → 拒绝
	_, err = db.Exec(`INSERT INTO datasources
		(tenant_id, name, engine_family, connect_mode, host, port)
		VALUES ($1, 'bad-connector', 'postgres', 'connector', 'h', 5432)`, devTenantID)
	if err == nil {
		t.Fatalf("connector mode without connector_id accepted, want CHECK violation")
	}

	// group_id 与 group_role 半空 → 拒绝
	mustExec(t, db, `INSERT INTO datasource_groups (tenant_id, id, name, kind) VALUES
		('`+devTenantID+`', '44444444-4444-4444-4444-444444444444', 'g1', 'cluster')`)
	_, err = db.Exec(`INSERT INTO datasources
		(tenant_id, name, engine_family, connect_mode, connector_id, host, port, group_id)
		VALUES ($1, 'bad-group', 'postgres', 'connector',
		        '55555555-5555-5555-5555-555555555555', 'h', 5432,
		        '44444444-4444-4444-4444-444444444444')`, devTenantID)
	if err == nil {
		t.Fatalf("group_id without group_role accepted, want CHECK violation")
	}
}

// verifyCrossTenantFK 复合外键拒绝跨租户引用（RLS 之外的第二道结构防线）。
func verifyCrossTenantFK(t *testing.T, db *sql.DB) {
	t.Helper()

	mustExec(t, db, `INSERT INTO tenants (id, name, slug) VALUES
		('`+tenantBID+`', '租户B', 'tenant-b') ON CONFLICT DO NOTHING`)

	// 租户 B 的 datasource 引用 dev 租户的 connector（verifyModeChecks 建）→ 复合 FK 拒绝
	_, err := db.Exec(`INSERT INTO datasources
		(tenant_id, name, engine_family, connect_mode, connector_id, host, port)
		VALUES ($1, 'steal', 'postgres', 'connector',
		        '55555555-5555-5555-5555-555555555555', 'h', 5432)`, tenantBID)
	if err == nil {
		t.Fatalf("cross-tenant connector reference accepted, want composite FK violation")
	}
}
