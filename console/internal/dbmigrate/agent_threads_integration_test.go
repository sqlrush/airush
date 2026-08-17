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

// TestAgentThreadsMigration spec-1.8 T1：0006 up→down→up 幂等；四表 RLS 四要素；
// 事件表按月分区（当月分区 + default 分区 + 幂等建分区函数）；跨租户不可见；
// 事件表对应用角色只增不改；agents.default_model 列就位。
func TestAgentThreadsMigration(t *testing.T) {
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
	downTo(t, db, pg.ConnString, 5)
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.tables
		WHERE table_name IN ('agent_threads','agent_rollout_events','agent_thread_queue','agent_graph_edges')`); n != 0 {
		t.Fatalf("down 后 agent 表仍在: %d", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'agents' AND column_name = 'default_model'`); n != 0 {
		t.Fatalf("down 后 agents.default_model 仍在")
	}
	if err := RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("re-up: %v", err)
	}

	// 四要素（分区父表也要 ENABLE/FORCE + policy）
	for _, tbl := range []string{"agent_threads", "agent_rollout_events", "agent_thread_queue", "agent_graph_edges"} {
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
	// agents.default_model
	if n := countRows(t, db, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'agents' AND column_name = 'default_model'`); n != 1 {
		t.Fatalf("agents.default_model 缺失")
	}
	// 分区：当月分区 + default 分区存在；函数幂等
	if n := countRows(t, db, `SELECT count(*) FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'agent_rollout_events'`); n < 4 {
		t.Fatalf("事件表分区数 = %d, want >= 4（当月 + 后两月 + default）", n)
	}
	var first, second string
	if err := db.QueryRow(`SELECT agent_rollout_events_ensure_partition(date_trunc('month', now())::date)`).Scan(&first); err != nil {
		t.Fatalf("ensure partition: %v", err)
	}
	if err := db.QueryRow(`SELECT agent_rollout_events_ensure_partition(date_trunc('month', now())::date)`).Scan(&second); err != nil {
		t.Fatalf("ensure partition twice: %v", err)
	}
	if first != second || !strings.HasPrefix(first, "agent_rollout_events_") {
		t.Fatalf("ensure partition 不幂等: %q / %q", first, second)
	}

	// 跨租户不可见 + 事件只增不改
	mustExec(t, db, `INSERT INTO tenants (id, name, slug) VALUES ('`+tenantBID+`', '租户B', 'tenant-b')
		ON CONFLICT DO NOTHING`)
	for i, tid := range []string{devTenantID, tenantBID} {
		thread := "0000000" + string(rune('1'+i)) + "-0000-7000-8000-000000000001"
		mustExec(t, db, `INSERT INTO agent_threads (tenant_id, id, model) VALUES ('`+tid+`', '`+thread+`', 'chat-default')`)
		mustExec(t, db, `INSERT INTO agent_rollout_events (tenant_id, thread_id, seq, event_type, payload)
			VALUES ('`+tid+`', '`+thread+`', 1, 'session_configured', '{}')`)
		mustExec(t, db, `INSERT INTO agent_thread_queue (tenant_id, thread_id, id, kind, payload)
			VALUES ('`+tid+`', '`+thread+`', gen_random_uuid(), 'steer', '{}')`)
	}
	tx := beginAsApp(t, db, devTenantID)
	defer func() { _ = tx.Rollback() }()
	var threads, events, queued int
	if err := tx.QueryRow(`SELECT (SELECT count(*) FROM agent_threads), (SELECT count(*) FROM agent_rollout_events),
		(SELECT count(*) FROM agent_thread_queue)`).Scan(&threads, &events, &queued); err != nil {
		t.Fatalf("count as app: %v", err)
	}
	if threads != 1 || events != 1 || queued != 1 {
		t.Fatalf("dev 租户视角 threads=%d events=%d queue=%d, want 1/1/1（跨租户可见 = P0）", threads, events, queued)
	}
	txExec(t, tx, "SAVEPOINT before_update")
	if _, err := tx.Exec(`UPDATE agent_rollout_events SET payload = '{"x":1}'`); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("应用角色不该能改事件（审计语义）: %v", err)
	}
	txExec(t, tx, "ROLLBACK TO SAVEPOINT before_update")
	txExec(t, tx, "SAVEPOINT before_delete")
	if _, err := tx.Exec(`DELETE FROM agent_rollout_events`); err == nil ||
		!strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("应用角色不该能删事件: %v", err)
	}
	txExec(t, tx, "ROLLBACK TO SAVEPOINT before_delete")
	// 应用角色可经父表写入事件（分区路由 + policy 放行本租户）
	if _, err := tx.Exec(`INSERT INTO agent_rollout_events (tenant_id, thread_id, seq, event_type, payload)
		VALUES ('` + devTenantID + `', '00000001-0000-7000-8000-000000000001', 2, 'turn_started', '{}')`); err != nil {
		t.Fatalf("应用角色写入本租户事件: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO agent_rollout_events (tenant_id, thread_id, seq, event_type, payload)
		VALUES ('` + tenantBID + `', '00000002-0000-7000-8000-000000000001', 2, 'turn_started', '{}')`); err == nil {
		t.Fatalf("应用角色不该能写别的租户的事件（policy 拒）")
	}
}
