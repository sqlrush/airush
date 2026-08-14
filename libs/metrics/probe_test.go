package metrics

import (
	"context"
	"errors"
	"testing"
)

// fakeQuerier returns scripted values/errors per SQL substring match (by index).
type fakeQuerier struct {
	values  map[string]float64
	absent  map[string]bool
	failing map[string]bool
	calls   int
	writes  int // increments if a non-SELECT is ever seen (should stay 0)
}

func (f *fakeQuerier) QueryMetricValue(_ context.Context, sql string) (float64, bool, error) {
	f.calls++
	if len(sql) < 6 || sql[:6] != "SELECT" {
		f.writes++
	}
	if f.failing[sql] {
		return 0, false, errors.New("boom")
	}
	if f.absent[sql] {
		return 0, false, nil
	}
	if v, ok := f.values[sql]; ok {
		return v, true, nil
	}
	return 1, true, nil // default present value
}

func TestProbeCollectFull(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{}
	batch, err := Probe{DatasourceID: "ds-1", EngineFamily: "postgres"}.Collect(context.Background(), q)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if batch.Partial {
		t.Fatal("full collect marked partial")
	}
	if len(batch.Metrics) != len(PostgresCatalog) {
		t.Fatalf("metrics = %d, want %d", len(batch.Metrics), len(PostgresCatalog))
	}
	if batch.CatalogVersion != CatalogVersion {
		t.Fatalf("catalog version = %d", batch.CatalogVersion)
	}
	// 只读契约：探针从不发非 SELECT 语句（T9 单测面）
	if q.writes != 0 {
		t.Fatalf("probe issued %d non-SELECT statements", q.writes)
	}
	// Labels 白名单：datasource_id 允许
	for _, m := range batch.Metrics {
		if m.Labels["datasource_id"] != "ds-1" {
			t.Fatalf("missing datasource_id label: %+v", m.Labels)
		}
	}
}

func TestProbeCollectPartial(t *testing.T) {
	t.Parallel()
	// 让复制延迟指标（主库无值）与一条 failing 指标缺采。
	// 按名查而非按下标：目录中间插入新条目会静默换掉被测对象，注释还留在原处
	// （2026-08-14 加 db.replication.lag_seconds 时正好踩到）。
	q := &fakeQuerier{
		absent:  map[string]bool{catalogSQL(t, "pg.replication.lag_bytes"): true},
		failing: map[string]bool{catalogSQL(t, "db.locks.waiting"): true},
	}
	batch, err := Probe{DatasourceID: "ds-1", EngineFamily: "postgres"}.Collect(context.Background(), q)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !batch.Partial || len(batch.Missing) != 2 {
		t.Fatalf("partial=%v missing=%v", batch.Partial, batch.Missing)
	}
	if len(batch.Metrics) != len(PostgresCatalog)-2 {
		t.Fatalf("metrics = %d, want %d", len(batch.Metrics), len(PostgresCatalog)-2)
	}
}

func TestProbeCollectAllFailIsError(t *testing.T) {
	t.Parallel()
	failing := map[string]bool{}
	for _, e := range PostgresCatalog {
		failing[e.SQL] = true
		if e.AltSQL != "" {
			failing[e.AltSQL] = true // 方言回退也失败，才算整批失败
		}
	}
	q := &fakeQuerier{failing: failing}
	if _, err := (Probe{DatasourceID: "ds", EngineFamily: "postgres"}).Collect(context.Background(), q); err == nil {
		t.Fatal("all-fail collect should error")
	}
}

// TestProbeAltSQLFallback：主 SQL 报错（函数不存在的方言差异）时改用 AltSQL，
// 该指标照采不缺（openGauss 的 pg_last_xlog_* 一族）。
func TestProbeAltSQLFallback(t *testing.T) {
	t.Parallel()
	var withAlt CatalogEntry
	for _, e := range PostgresCatalog {
		if e.AltSQL != "" {
			withAlt = e
			break
		}
	}
	if withAlt.Name == "" {
		t.Skip("catalog has no dialect fallback entry")
	}

	q := &fakeQuerier{failing: map[string]bool{withAlt.SQL: true}}
	batch, err := Probe{DatasourceID: "ds", EngineFamily: "postgres"}.Collect(context.Background(), q)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	for _, name := range batch.Missing {
		if name == withAlt.Name {
			t.Fatalf("%s should have been collected through AltSQL", withAlt.Name)
		}
	}
}

func TestProbeNoCatalog(t *testing.T) {
	t.Parallel()
	_, err := Probe{DatasourceID: "ds", EngineFamily: "mysql"}.Collect(context.Background(), &fakeQuerier{})
	if !errors.Is(err, ErrNoCatalog) {
		t.Fatalf("err = %v, want ErrNoCatalog", err)
	}
}

func TestSanitizeLabelsWhitelist(t *testing.T) {
	t.Parallel()
	clean, dropped := sanitizeLabels(map[string]string{
		"datasource_id": "ds", "database": "prod",
		"query_text": "SELECT * FROM secrets", // 非白名单：必须丢弃
	})
	if _, ok := clean["query_text"]; ok {
		t.Fatal("non-whitelisted label survived")
	}
	if len(dropped) != 1 || dropped[0] != "query_text" {
		t.Fatalf("dropped = %v", dropped)
	}
	if clean["datasource_id"] != "ds" || clean["database"] != "prod" {
		t.Fatalf("whitelisted labels lost: %v", clean)
	}
}

func TestCatalogSQLAreReadOnly(t *testing.T) {
	t.Parallel()
	// 目录静态检查：每条 SQL 以 SELECT 开头、无写关键字（AD-3 只读契约，T9 静态面）
	for _, e := range PostgresCatalog {
		if len(e.SQL) < 6 || e.SQL[:6] != "SELECT" {
			t.Fatalf("%s: SQL not a SELECT: %q", e.Name, e.SQL)
		}
		for _, bad := range []string{"INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE", "TRUNCATE"} {
			if containsWord(e.SQL, bad) {
				t.Fatalf("%s: SQL contains write keyword %q", e.Name, bad)
			}
		}
	}
}

func containsWord(s, word string) bool {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] == word {
			return true
		}
	}
	return false
}

// catalogSQL 按目录名取该条目的主 SQL；找不到即失败（防改名后测试静默失准）。
func catalogSQL(t *testing.T, name string) string {
	t.Helper()
	for _, e := range PostgresCatalog {
		if e.Name == name {
			return e.SQL
		}
	}
	t.Fatalf("目录里没有 %s —— 是否改名后忘了同步测试", name)
	return ""
}
