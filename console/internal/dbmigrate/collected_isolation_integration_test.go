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

// 本文件是 AD-10「等效隔离形态」的准入门槛（spec-1.5 §2.5，T7-T10）。
//
// 背景：TimescaleDB 列存压缩与 RLS 在同一张表互斥，故 tsdb.series 不挂 RLS，
// 改由「基表零授权 + security_barrier 视图 + check_option」承载隔离。AD-10 修订时
// 定死四项门槛，任一不过则该形态作废、spec-1.5 不可上线——因此这四条排在实施计划
// 第 1 步，先证伪最贵的假设。
//
// ④ 在 probe-timescale-rls2.sh 初验时**没拦住**（security_barrier 只管读不管写，
// 补 check_option = cascaded 才堵上）。等效形态有不显眼的缺口，逐项验证不是形式主义。

const (
	tsSeriesInsert = `INSERT INTO collected.series
		(tenant_id, datasource_id, series_name, entity_id, value, at) VALUES`
	dsA = "aaaaaaaa-0000-0000-0000-00000000000a"
	dsB = "bbbbbbbb-0000-0000-0000-00000000000b"
)

// TestCollectedIsolation spec-1.5 T7-T10：等效隔离四项门槛，一个用例内串验
// （共用一套 fixture，且四项本就是同一形态的四个面）。
func TestCollectedIsolation(t *testing.T) {
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	seedTwoTenantSeries(t, admin)

	t.Run("T7_压缩启用下经视图只见本租户", func(t *testing.T) {
		compressAllSeriesChunks(t, admin)

		tx := beginAsApp(t, admin, devTenantID)
		defer func() { _ = tx.Rollback() }()

		var rows, tenants int
		if err := tx.QueryRow(`SELECT count(*), count(DISTINCT tenant_id)
			FROM collected.series`).Scan(&rows, &tenants); err != nil {
			t.Fatalf("query via view: %v", err)
		}
		if tenants != 1 {
			t.Fatalf("visible tenants = %d, want 1（跨租户可见 = P0）", tenants)
		}
		if rows == 0 {
			t.Fatal("visible rows = 0，本租户数据也看不到——视图谓词写错了")
		}
		// 压缩确实生效才算验到点上：未压缩的话这条门槛等于没验。
		if n := countRows(t, admin, `SELECT count(*) FROM timescaledb_information.chunks
			WHERE hypertable_schema = 'tsdb' AND is_compressed`); n == 0 {
			t.Fatal("没有已压缩 chunk——本用例未真正覆盖『压缩下的隔离』")
		}
	})

	t.Run("T8_无租户上下文返回0行而非报错", func(t *testing.T) {
		tx, err := admin.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		txExec(t, tx, "SET LOCAL ROLE airush_app") // 刻意不设 app.tenant_id

		var rows int
		if err := tx.QueryRow(`SELECT count(*) FROM collected.series`).Scan(&rows); err != nil {
			t.Fatalf("无租户上下文查询应返回 0 行而非报错，实际报错: %v", err)
		}
		if rows != 0 {
			t.Fatalf("无租户上下文可见 %d 行，want 0（fail-closed）", rows)
		}
	})

	t.Run("T9_应用角色绕过视图直读基表被拒", func(t *testing.T) {
		tx := beginAsApp(t, admin, devTenantID)
		defer func() { _ = tx.Rollback() }()

		var n int
		err := tx.QueryRow(`SELECT count(*) FROM tsdb.series`).Scan(&n)
		if err == nil {
			t.Fatalf("airush_app 直读基表成功（%d 行），want 权限拒绝——"+
				"tsdb schema 的 USAGE 被误授了", n)
		}
		// 双锁第一道是 schema 无 USAGE，故报错应为 schema 级而非表级。
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("拒绝原因不是权限问题: %v", err)
		}
	})

	t.Run("T10_经视图伪造他人tenant_id写入被拒", func(t *testing.T) {
		tx := beginAsApp(t, admin, devTenantID)
		defer func() { _ = tx.Rollback() }()

		// 越权写会把事务置为 aborted，后续语句一律 25P02。用 savepoint 隔离这次失败，
		// 否则下面的反证量到的是"事务已废"而不是"写入被拒"。
		txExec(t, tx, "SAVEPOINT before_cross_tenant")
		_, err := tx.Exec(tsSeriesInsert+`
			($1, $2, 'db.connections.active', '', 1, now())`, tenantBID, dsB)
		if err == nil {
			t.Fatal("越权写入未被拦截——check_option 缺失或失效。" +
				"security_barrier 只管读不管写，这条初验时漏过一次")
		}
		if !strings.Contains(err.Error(), "check option") {
			t.Fatalf("拒绝原因不是 check option: %v", err)
		}
		txExec(t, tx, "ROLLBACK TO SAVEPOINT before_cross_tenant")

		// 反证：写本租户必须成功，否则上面的"拒绝"可能只是视图整体不可写，
		// 那样这条门槛就是假过。
		if _, err := tx.Exec(tsSeriesInsert+`
			($1, $2, 'db.connections.active', '', 1, now())`, devTenantID, dsA); err != nil {
			t.Fatalf("本租户写入被误拒: %v", err)
		}
	})
}

// TestCollectedEntitiesSnapshotsRLS spec-1.5：两张普通表走 spec-0.6 §2.2 标准 RLS 模板，
// 与超表的等效形态并存——验证"只有超表用等效形态"这条边界没被扩大。
func TestCollectedEntitiesSnapshotsRLS(t *testing.T) {
	ctx := context.Background()

	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	admin, err := sql.Open("pgx", pg.ConnString)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	seedTwoTenantSeries(t, admin)

	for _, tbl := range []string{"collected.entities", "collected.snapshots"} {
		var enabled, forced bool
		q := `SELECT relrowsecurity, relforcerowsecurity FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'collected' AND c.relname = $1`
		name := tbl[len("collected."):]
		if err := admin.QueryRow(q, name).Scan(&enabled, &forced); err != nil {
			t.Fatalf("%s: 读 RLS 标志: %v", tbl, err)
		}
		if !enabled || !forced {
			t.Fatalf("%s: ENABLE=%v FORCE=%v，模板四要素缺项", tbl, enabled, forced)
		}
	}

	// 跨租户不可见（entities 已由 seed 各租户各写一条）
	tx := beginAsApp(t, admin, devTenantID)
	defer func() { _ = tx.Rollback() }()
	var tenants int
	if err := tx.QueryRow(`SELECT count(DISTINCT tenant_id) FROM collected.entities`).
		Scan(&tenants); err != nil {
		t.Fatalf("query entities: %v", err)
	}
	if tenants != 1 {
		t.Fatalf("entities visible tenants = %d, want 1", tenants)
	}
}

// seedTwoTenantSeries 预置两个租户各一个数据源与若干读数/实体（超级用户身份，绕过 RLS）。
func seedTwoTenantSeries(t *testing.T, admin *sql.DB) {
	t.Helper()

	mustExec(t, admin, `INSERT INTO tenants (id, name, slug)
		VALUES ('`+tenantBID+`', '租户B', 'tenant-b') ON CONFLICT DO NOTHING`)
	// datasources 的 mode_direct_shape CHECK 要求 direct 模式必须带 credential_id，
	// 故先建凭据（内容是占位密文，本用例不解密）。
	for _, spec := range []struct{ tenant, ds, cred, name string }{
		{devTenantID, dsA, "cccccccc-0000-0000-0000-00000000000a", "ds-a"},
		{tenantBID, dsB, "cccccccc-0000-0000-0000-00000000000b", "ds-b"},
	} {
		mustExec(t, admin, `INSERT INTO datasource_credentials
			(tenant_id, id, username, secret_ciphertext, key_id)
			VALUES ('`+spec.tenant+`', '`+spec.cred+`', 'probe', '\x00'::bytea, 'k1')`)
		mustExec(t, admin, `INSERT INTO datasources
			(tenant_id, id, name, engine_family, connect_mode, credential_id, host, port)
			VALUES ('`+spec.tenant+`', '`+spec.ds+`', '`+spec.name+`', 'postgres',
				'direct', '`+spec.cred+`', 'h', 5432)`)
	}

	// 读数：两租户各 3 天数据，落在不同 chunk 上以便压缩验证有实际对象。
	mustExec(t, admin, `INSERT INTO tsdb.series
		(tenant_id, datasource_id, series_name, entity_id, value, at)
		SELECT '`+devTenantID+`', '`+dsA+`', 'db.connections.active', '', 10,
			now() - (n || ' hours')::interval FROM generate_series(0, 71) n`)
	mustExec(t, admin, `INSERT INTO tsdb.series
		(tenant_id, datasource_id, series_name, entity_id, value, at)
		SELECT '`+tenantBID+`', '`+dsB+`', 'db.connections.active', '', 20,
			now() - (n || ' hours')::interval FROM generate_series(0, 71) n`)

	for _, spec := range []struct{ tenant, ds string }{{devTenantID, dsA}, {tenantBID, dsB}} {
		mustExec(t, admin, `INSERT INTO collected.entities
			(tenant_id, datasource_id, entity_kind, entity_id, label, first_seen_at, last_seen_at)
			VALUES ('`+spec.tenant+`', '`+spec.ds+`', 'query', 'e1', 'SELECT $1', now(), now())`)
	}
}

// compressAllSeriesChunks 压缩全部够老的 chunk（now 之前的都够老——策略延迟不参与用例）。
func compressAllSeriesChunks(t *testing.T, admin *sql.DB) {
	t.Helper()
	mustExec(t, admin, `SELECT compress_chunk(c, if_not_compressed => true)
		FROM show_chunks('tsdb.series', older_than => INTERVAL '1 hour') c`)
}

// beginAsApp 开一个降权到 airush_app 且设定租户 GUC 的事务——与 repo.InTenantTx 同形态。
func beginAsApp(t *testing.T, admin *sql.DB, tenantID string) *sql.Tx {
	t.Helper()
	tx, err := admin.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	txExec(t, tx, "SET LOCAL ROLE airush_app")
	if _, err := tx.Exec(`SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatalf("set tenant guc: %v", err)
	}
	return tx
}
