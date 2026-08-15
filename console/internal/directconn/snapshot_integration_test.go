//go:build integration

// spec-1.4 Direct 通道端到端：真实 PG（testcontainers）与真实 openGauss（外部实例，
// 环境变量接入）→ directconn.SnapshotQuerier → SnapshotProbe.Collect。
// 覆盖 T1（慢查询目录有效）/T2（值合理域与规范化形态）/T4（能力缺失降级）/
// T5（表结构快照）/T6（配置快照）/T9（只读）/T10（文本截断在单测）。
package directconn_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/libs/metrics"
)

// TestSnapshotConfigOnPostgres：pg_settings 全量快照（T6）。
func TestSnapshotConfigOnPostgres(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	snap := collectSnapshot(t, e, id, metrics.SnapshotKindConfig)
	if snap.CapabilityMissing {
		t.Fatal("pg_settings is always available")
	}
	if snap.Source != "pg_settings" || len(snap.Configs) < 100 {
		t.Fatalf("config snapshot = source %q, %d entries", snap.Source, len(snap.Configs))
	}
	byName := map[string]metrics.ConfigEntry{}
	for _, c := range snap.Configs {
		byName[c.Name] = c
	}
	for _, want := range []string{"max_connections", "shared_buffers"} {
		if entry, ok := byName[want]; !ok || entry.Value == "" {
			t.Fatalf("missing key setting %q: %+v", want, entry)
		}
	}
}

// TestSnapshotSchemaOnPostgres：预置表 → 列/索引/大小齐备，系统 schema 排除（T5）。
func TestSnapshotSchemaOnPostgres(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)
	execOnTarget(t, e, id, `CREATE TABLE IF NOT EXISTS snap_probe_t (
		id bigint PRIMARY KEY, note text, created_at timestamptz NOT NULL DEFAULT now())`)
	execOnTarget(t, e, id, `CREATE INDEX IF NOT EXISTS snap_probe_t_note_idx ON snap_probe_t (note)`)

	snap := collectSnapshot(t, e, id, metrics.SnapshotKindSchema)
	var target *metrics.TableInfo
	for i := range snap.Tables {
		if snap.Tables[i].Name == "snap_probe_t" {
			target = &snap.Tables[i]
		}
		if snap.Tables[i].Schema == "pg_catalog" || snap.Tables[i].Schema == "information_schema" {
			t.Fatalf("system schema leaked into the snapshot: %+v", snap.Tables[i])
		}
	}
	if target == nil {
		t.Fatalf("snap_probe_t missing from %d tables", len(snap.Tables))
	}

	cols := map[string]metrics.ColumnInfo{}
	for _, c := range target.Columns {
		cols[c.Name] = c
	}
	if len(cols) != 3 {
		t.Fatalf("columns = %+v, want 3", target.Columns)
	}
	if cols["id"].Nullable || !cols["note"].Nullable {
		t.Fatalf("nullability wrong: %+v", cols)
	}
	if !strings.Contains(cols["id"].DataType, "bigint") {
		t.Fatalf("id data type = %q", cols["id"].DataType)
	}

	var unique, secondary bool
	for _, idx := range target.Indexes {
		if idx.IsUnique && len(idx.Columns) == 1 && idx.Columns[0] == "id" {
			unique = true
		}
		if idx.Name == "snap_probe_t_note_idx" && len(idx.Columns) == 1 && idx.Columns[0] == "note" {
			secondary = true
		}
	}
	if !unique || !secondary {
		t.Fatalf("indexes = %+v", target.Indexes)
	}
}

// TestSnapshotSlowlogCapabilityMissingOnPostgres：未装 pg_stat_statements 的原生 PG
// 走结构化降级而非报错（T4）。testcontainers 起的 PG 默认不带该扩展。
func TestSnapshotSlowlogCapabilityMissingOnPostgres(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	snap := collectSnapshot(t, e, id, metrics.SnapshotKindSlowlog)
	if !snap.CapabilityMissing {
		t.Fatalf("expected capability degradation without pg_stat_statements, got source %q with %d entries",
			snap.Source, len(snap.SlowQueries))
	}
	if len(snap.SlowQueries) != 0 || snap.Kind != metrics.SnapshotKindSlowlog {
		t.Fatalf("degraded snapshot should stay empty and well-formed: %+v", snap)
	}
}

// TestSnapshotUnsupportedKind：未知 kind 显式拒绝（T8 的探针侧）。
func TestSnapshotUnsupportedKind(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	probe := metrics.SnapshotProbe{DatasourceID: id, EngineFamily: "postgres"}
	if _, err := probe.Collect(e.tenant, e.mgr.SnapshotQuerier(id), "rowdump"); err == nil {
		t.Fatal("expected an unsupported-kind rejection")
	}
}

func collectSnapshot(t *testing.T, e *env, datasourceID, kind string) metrics.Snapshot {
	t.Helper()
	probe := metrics.SnapshotProbe{DatasourceID: datasourceID, EngineFamily: "postgres"}
	snap, err := probe.Collect(e.tenant, e.mgr.SnapshotQuerier(datasourceID), kind)
	if err != nil {
		t.Fatalf("collect %s snapshot: %v", kind, err)
	}
	if snap.DatasourceID != datasourceID || snap.Kind != kind || snap.CatalogVersion == 0 {
		t.Fatalf("snapshot envelope = %+v", snap)
	}
	return snap
}

// execOnTarget 在被采集库上执行 DDL（仅测试夹具用；探针本身只读）。
func execOnTarget(t *testing.T, e *env, datasourceID, ddl string) {
	t.Helper()
	q, ok := e.mgr.SnapshotQuerier(datasourceID).(interface {
		QueryRows(context.Context, string, int) ([]map[string]string, error)
	})
	if !ok {
		t.Fatal("snapshot querier does not expose QueryRows")
	}
	if _, err := q.QueryRows(e.tenant, ddl, 1); err != nil {
		t.Fatalf("fixture ddl %q: %v", ddl, err)
	}
}

// --- 真实 openGauss（外部实例）---
//
// openGauss 无法用 testcontainers 便捷起（镜像大、初始化慢、密码策略特殊），故以
// 环境变量接外部实例：AIRUSH_OPENGAUSS_HOST/PORT/PASSWORD。未设置即跳过——这不是
// 为过 CI 而跳过失败用例，而是该用例需要 CI 不具备的外部依赖；本地/验收环境按
// spec-1.4 §7 DoD 与 spec-1.16 真机验收执行。

type ogTarget struct {
	host     string
	port     int
	user     string
	database string
	password string
}

func openGaussTarget(t *testing.T) (ogTarget, bool) {
	t.Helper()
	target := ogTarget{
		host:     os.Getenv("AIRUSH_OPENGAUSS_HOST"),
		port:     5432,
		user:     envOr("AIRUSH_OPENGAUSS_USER", "gaussdb"),
		database: envOr("AIRUSH_OPENGAUSS_DB", "postgres"),
		password: os.Getenv("AIRUSH_OPENGAUSS_PASSWORD"),
	}
	if target.host == "" || target.password == "" {
		t.Skip("openGauss 未接入（设 AIRUSH_OPENGAUSS_HOST/PORT/USER/PASSWORD 后运行）")
		return ogTarget{}, false
	}
	if raw := os.Getenv("AIRUSH_OPENGAUSS_PORT"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &target.port); err != nil {
			t.Fatalf("bad AIRUSH_OPENGAUSS_PORT %q: %v", raw, err)
		}
	}
	return target, true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// createOpenGaussDatasource 建指向外部 openGauss 的直连数据源（用户名/库名可配，
// 不同于内置 PG 夹具写死的 postgres/postgres）。
func createOpenGaussDatasource(t *testing.T, e *env, target ogTarget) string {
	t.Helper()
	var dsID string
	err := e.store.InTenantTx(e.tenant, func(ctx context.Context, tx repo.Tx) error {
		ciphertext, err := e.sealer.Seal([]byte(target.password))
		if err != nil {
			return err
		}
		credID, err := repo.InsertCredential(ctx, tx, target.user, ciphertext, "v1")
		if err != nil {
			return err
		}
		ds, err := repo.InsertDatasource(ctx, tx, repo.DatasourceInput{
			Name: "og-" + target.host, EngineFamily: "postgres", ConnectMode: "direct",
			CredentialID: &credID, Host: target.host, Port: target.port,
			DatabaseName: target.database,
		})
		dsID = ds.ID
		return err
	})
	if err != nil {
		t.Fatalf("create openGauss datasource: %v", err)
	}
	return dsID
}

// TestSnapshotSlowlogOnOpenGauss：openGauss 走 dbe_perf 候选，且上报文本必须是
// **规范化**语句——这是 AD-3 在 spec-1.6 脱敏引擎之前的编译期防线（T1/T2/T3）。
func TestSnapshotSlowlogOnOpenGauss(t *testing.T) {
	target, ok := openGaussTarget(t)
	if !ok {
		return
	}
	e := newEnv(t, directconn.DefaultConfig())
	id := createOpenGaussDatasource(t, e, target)

	// T3：先执行一条带唯一字面量的语句，随后断言该字面量不出现在上报里。
	const literal = "airush_literal_canary_20260812"
	execOnTarget(t, e, id, fmt.Sprintf("SELECT count(*) FROM pg_class WHERE relname = '%s'", literal))

	snap := collectSnapshot(t, e, id, metrics.SnapshotKindSlowlog)
	if snap.CapabilityMissing {
		t.Fatal("openGauss should expose dbe_perf")
	}
	if snap.Source != "dbe_perf" {
		t.Fatalf("Source = %q, want dbe_perf", snap.Source)
	}
	if len(snap.SlowQueries) == 0 {
		t.Fatal("expected at least one tracked statement")
	}
	for _, entry := range snap.SlowQueries {
		if strings.Contains(entry.Text, literal) {
			t.Fatalf("raw literal leaked into the reported statement: %q", entry.Text)
		}
		if entry.Calls < 1 || entry.MeanMs < 0 || entry.TotalMs < 0 {
			t.Fatalf("implausible entry: %+v", entry)
		}
		if len([]rune(entry.Text)) > metrics.QueryTextMaxLen {
			t.Fatalf("text exceeds the cap: %d runes", len([]rune(entry.Text)))
		}
	}
}

// TestSnapshotSchemaAndConfigOnOpenGauss：表结构/配置目录在 openGauss 上可执行（T5/T6）。
func TestSnapshotSchemaAndConfigOnOpenGauss(t *testing.T) {
	target, ok := openGaussTarget(t)
	if !ok {
		return
	}
	e := newEnv(t, directconn.DefaultConfig())
	id := createOpenGaussDatasource(t, e, target)

	schema := collectSnapshot(t, e, id, metrics.SnapshotKindSchema)
	if len(schema.Tables) == 0 {
		t.Fatal("expected tables from openGauss")
	}
	// 按大小降序（数值序而非字符串序）
	for i := 1; i < len(schema.Tables); i++ {
		if schema.Tables[i-1].SizeBytes < schema.Tables[i].SizeBytes {
			t.Fatalf("tables not ordered by size: %d before %d",
				schema.Tables[i-1].SizeBytes, schema.Tables[i].SizeBytes)
		}
	}

	config := collectSnapshot(t, e, id, metrics.SnapshotKindConfig)
	if len(config.Configs) < 100 {
		t.Fatalf("openGauss config entries = %d", len(config.Configs))
	}
}

// TestMetricsOnOpenGauss 补 spec-1.3 DoD 欠下的 openGauss 侧验证：指标目录在
// openGauss 上应整体可采，复制延迟走 AltSQL 的 9.2 系方言（pg_last_xlog_*）。
func TestMetricsOnOpenGauss(t *testing.T) {
	target, ok := openGaussTarget(t)
	if !ok {
		return
	}
	e := newEnv(t, directconn.DefaultConfig())
	id := createOpenGaussDatasource(t, e, target)

	probe := metrics.Probe{DatasourceID: id, EngineFamily: "postgres"}
	batch, err := probe.Collect(e.tenant, e.mgr.MetricsQuerier(id))
	if err != nil {
		t.Fatalf("collect metrics on openGauss: %v", err)
	}

	byName := map[string]metrics.Metric{}
	for _, m := range batch.Metrics {
		byName[m.Name] = m
	}
	// 主库上复制延迟无值（Nullable），其余指标都应采到。
	for _, entry := range metrics.PostgresCatalog {
		if entry.Nullable {
			continue
		}
		if _, ok := byName[entry.Name]; !ok {
			t.Fatalf("metric %q missing on openGauss (missing set: %v)", entry.Name, batch.Missing)
		}
	}
	if m := byName["db.cache.hit_ratio"]; m.Value < 0 || m.Value > 1 {
		t.Fatalf("cache hit ratio out of domain: %v", m.Value)
	}
	// 复制延迟类缺采是允许的（主库无上游），但**不允许**因为函数不存在而报错——
	// AltSQL 已覆盖；若 Missing 里有它，说明走的是"无值"分支而非执行失败，
	// 由上面的 Collect 成功保证。
	//
	// 允许缺采的集合由目录的 Nullable 标志推导，不写死名字：写死会在目录新增
	// Nullable 指标时无声变红（2026-08-14 加 db.replication.lag_seconds 时正好如此）。
	nullable := map[string]bool{}
	for _, entry := range metrics.PostgresCatalog {
		if entry.Nullable {
			nullable[entry.Name] = true
		}
	}
	for _, name := range batch.Missing {
		if !nullable[name] {
			t.Fatalf("unexpected missing metric on openGauss: %s", name)
		}
	}
}
