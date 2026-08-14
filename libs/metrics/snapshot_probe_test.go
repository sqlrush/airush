package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRowQuerier 按 SQL 子串匹配回放预置行，并记录执行过的 SQL。
type fakeRowQuerier struct {
	// responses 的 key 是 SQL 的匹配子串。
	responses map[string][]map[string]string
	failOn    map[string]error
	executed  []string
}

func (f *fakeRowQuerier) QueryRows(_ context.Context, sql string, maxRows int) ([]map[string]string, error) {
	f.executed = append(f.executed, sql)
	for needle, err := range f.failOn {
		if strings.Contains(sql, needle) {
			return nil, err
		}
	}
	for needle, rows := range f.responses {
		if strings.Contains(sql, needle) {
			if maxRows > 0 && len(rows) > maxRows {
				rows = rows[:maxRows]
			}
			return rows, nil
		}
	}
	return nil, nil
}

func TestSnapshotProbeRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}
	_, err := probe.Collect(context.Background(), &fakeRowQuerier{}, "rowdump")
	if !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("Collect(rowdump) = %v, want ErrUnsupportedKind", err)
	}
}

func TestSnapshotProbeRejectsUnknownEngine(t *testing.T) {
	t.Parallel()
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "mysql"}
	_, err := probe.Collect(context.Background(), &fakeRowQuerier{}, SnapshotKindConfig)
	if !errors.Is(err, ErrNoSnapshotCatalog) {
		t.Fatalf("Collect(mysql) = %v, want ErrNoSnapshotCatalog", err)
	}
}

// TestSnapshotProbeSlowlogPrimarySource：pg_stat_statements 可用即取该源。
func TestSnapshotProbeSlowlogPrimarySource(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"pg_extension": {{"?column?": "1"}},
		"pg_stat_statements s": {
			{
				"query_id": "42", "text": "SELECT * FROM t WHERE id = $1", "calls": "10",
				"total_ms": "250.5", "mean_ms": "25.05", "max_ms": "80", "rows": "10", "database": "airush",
			},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snap.CapabilityMissing {
		t.Fatal("capability should be present when pg_stat_statements exists")
	}
	if snap.Source != "pg_stat_statements" {
		t.Fatalf("Source = %q", snap.Source)
	}
	if len(snap.SlowQueries) != 1 {
		t.Fatalf("SlowQueries = %d, want 1", len(snap.SlowQueries))
	}
	got := snap.SlowQueries[0]
	if got.QueryID != "42" || got.Calls != 10 || got.TotalMs != 250.5 || got.Rows != 10 {
		t.Fatalf("entry = %+v", got)
	}
	if !strings.Contains(got.Text, "$1") {
		t.Fatalf("expected a normalized statement, got %q", got.Text)
	}
}

// TestSnapshotProbeSlowlogFallsBackToDbePerf：主源缺失时走 openGauss 候选。
func TestSnapshotProbeSlowlogFallsBackToDbePerf(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"pg_extension":         {}, // 未装扩展
		"nspname = 'dbe_perf'": {{"?column?": "1"}},
		"dbe_perf.summary_statement": {
			{
				"query_id": "7", "text": "SELECT ?", "calls": "3", "total_ms": "9",
				"mean_ms": "3", "max_ms": "5", "rows": "3", "database": "postgres",
			},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snap.Source != "dbe_perf" || len(snap.SlowQueries) != 1 {
		t.Fatalf("snapshot = source %q, %d entries", snap.Source, len(snap.SlowQueries))
	}
}

// TestSnapshotProbeCapabilityMissing：全部候选不可用 → 结构化降级，非错误。
func TestSnapshotProbeCapabilityMissing(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("capability miss must be a success path, got %v", err)
	}
	if !snap.CapabilityMissing || len(snap.SlowQueries) != 0 || snap.Source != "" {
		t.Fatalf("snapshot = %+v, want CapabilityMissing with no entries", snap)
	}
	if snap.Kind != SnapshotKindSlowlog || snap.DatasourceID != "ds1" {
		t.Fatalf("envelope not filled: %+v", snap)
	}
}

// TestSnapshotProbeSourceErrorFallsThrough：源探测可用但采集失败 → 试下一个候选。
func TestSnapshotProbeSourceErrorFallsThrough(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{
		responses: map[string][]map[string]string{
			"pg_extension":         {{"?column?": "1"}},
			"nspname = 'dbe_perf'": {{"?column?": "1"}},
			"dbe_perf.summary_statement": {
				{"query_id": "7", "text": "SELECT ?", "calls": "1", "total_ms": "1"},
			},
		},
		failOn: map[string]error{"pg_stat_statements s": errors.New("column does not exist")},
	}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snap.Source != "dbe_perf" {
		t.Fatalf("Source = %q, want fallback to dbe_perf", snap.Source)
	}
}

// TestSnapshotProbeAllSourcesFail：候选都可用但都采集失败 → 报错（不冒充降级）。
func TestSnapshotProbeAllSourcesFail(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{
		responses: map[string][]map[string]string{
			"pg_extension":         {{"?column?": "1"}},
			"nspname = 'dbe_perf'": {{"?column?": "1"}},
		},
		failOn: map[string]error{
			"pg_stat_statements s":       errors.New("boom"),
			"dbe_perf.summary_statement": errors.New("boom"),
		},
	}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	if _, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog); err == nil {
		t.Fatal("expected an error when every available source fails")
	}
}

// TestSnapshotProbeTruncatesLongText：文本超长截断并逐条标记（尺寸有界）。
func TestSnapshotProbeTruncatesLongText(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", QueryTextMaxLen+500)
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"pg_extension": {{"?column?": "1"}},
		"pg_stat_statements s": {
			{"query_id": "1", "text": long, "calls": "1"},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	entry := snap.SlowQueries[0]
	if len([]rune(entry.Text)) != QueryTextMaxLen {
		t.Fatalf("text len = %d, want %d", len([]rune(entry.Text)), QueryTextMaxLen)
	}
	if !entry.Truncated || !snap.Truncated {
		t.Fatalf("truncation not flagged: entry=%v snapshot=%v", entry.Truncated, snap.Truncated)
	}
}

// TestSnapshotProbeSchemaAssembly：表/列/索引按 (schema,name) 正确装配。
func TestSnapshotProbeSchemaAssembly(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"SELECT nspname AS schema": {
			{"schema": "public", "name": "orders", "size_bytes": "8192", "row_estimate": "120.0"},
		},
		"a.attname AS column_name": {
			{"schema": "public", "name": "orders", "column_name": "id", "data_type": "bigint", "nullable": "false"},
			{"schema": "public", "name": "orders", "column_name": "note", "data_type": "text", "nullable": "true"},
			{"schema": "public", "name": "dropped", "column_name": "x", "data_type": "int", "nullable": "true"},
		},
		"ic.relname AS index_name": {
			{
				"schema": "public", "name": "orders", "index_name": "orders_pkey", "is_unique": "true",
				"index_def": "CREATE UNIQUE INDEX orders_pkey ON public.orders USING btree (id)",
			},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSchema)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snap.Tables) != 1 {
		t.Fatalf("Tables = %d, want 1 (rows for unlisted tables dropped)", len(snap.Tables))
	}
	table := snap.Tables[0]
	if table.SizeBytes != 8192 || table.RowEstimate != 120 {
		t.Fatalf("table stats = %+v", table)
	}
	if len(table.Columns) != 2 || table.Columns[0].Name != "id" || table.Columns[0].Nullable {
		t.Fatalf("columns = %+v", table.Columns)
	}
	if !table.Columns[1].Nullable {
		t.Fatal("note column should be nullable")
	}
	if len(table.Indexes) != 1 || !table.Indexes[0].IsUnique {
		t.Fatalf("indexes = %+v", table.Indexes)
	}
	if got := table.Indexes[0].Columns; len(got) != 1 || got[0] != "id" {
		t.Fatalf("index columns = %v, want [id]", got)
	}
}

func TestSnapshotProbeConfig(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"pg_settings": {
			{"name": "max_connections", "value": "100", "unit": "", "source": "configuration file"},
			{"name": "shared_buffers", "value": "16384", "unit": "8kB", "source": "configuration file"},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindConfig)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(snap.Configs) != 2 || snap.Configs[0].Name != "max_connections" {
		t.Fatalf("configs = %+v", snap.Configs)
	}
	if snap.Configs[1].Unit != "8kB" {
		t.Fatalf("unit not carried: %+v", snap.Configs[1])
	}
}

func TestParseIndexColumns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		def  string
		want []string
	}{
		{"CREATE UNIQUE INDEX pk ON public.t USING btree (id)", []string{"id"}},
		{"CREATE INDEX i ON public.t USING btree (a, b DESC)", []string{"a", "b DESC"}},
		{
			// 表达式索引：括号内的逗号不得被当作列分隔符。
			"CREATE INDEX i ON public.t USING btree (lower((a)::text), coalesce(b, c))",
			[]string{"lower((a)::text)", "coalesce(b, c)"},
		},
		{"malformed", nil},
	}
	for _, tc := range cases {
		got := parseIndexColumns(tc.def)
		if len(got) != len(tc.want) {
			t.Fatalf("parseIndexColumns(%q) = %v, want %v", tc.def, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseIndexColumns(%q)[%d] = %q, want %q", tc.def, i, got[i], tc.want[i])
			}
		}
	}
}

// TestEnforceSnapshotSize：超字节上限即从尾部成批丢弃并标记截断。
func TestEnforceSnapshotSize(t *testing.T) {
	t.Parallel()
	snap := Snapshot{Kind: SnapshotKindSlowlog}
	body := strings.Repeat("y", 2000)
	for i := 0; i < 400; i++ {
		snap.SlowQueries = append(snap.SlowQueries, SlowQueryEntry{QueryID: "q", Text: body})
	}
	if snapshotSize(&snap) <= SnapshotMaxBytes {
		t.Fatal("fixture should exceed the size limit")
	}

	enforceSnapshotSize(&snap)
	if snapshotSize(&snap) > SnapshotMaxBytes {
		t.Fatalf("size still %d after enforcement", snapshotSize(&snap))
	}
	if !snap.Truncated || len(snap.SlowQueries) == 0 {
		t.Fatalf("expected a truncated but non-empty snapshot, got %d entries", len(snap.SlowQueries))
	}
}

func TestValidSnapshotKind(t *testing.T) {
	t.Parallel()
	for _, kind := range SnapshotKinds {
		if !ValidSnapshotKind(kind) {
			t.Fatalf("%q should be valid", kind)
		}
	}
	for _, kind := range []string{"", "metrics", "SLOWLOG", "rowdump"} {
		if ValidSnapshotKind(kind) {
			t.Fatalf("%q should be rejected", kind)
		}
	}
}

// TestSnapshotCatalogHasNoRowData 逐条核对目录 SQL 只读且不碰业务表行数据。
func TestSnapshotCatalogHasNoRowData(t *testing.T) {
	t.Parallel()
	forbidden := []string{"insert ", "update ", "delete ", "drop ", "alter ", "create table", "pg_stat_activity"}
	for _, kind := range SnapshotKinds {
		for _, source := range snapshotSourcesFor("postgres", kind) {
			all := append([]snapshotQuery{}, source.Queries...)
			if source.ProbeSQL != "" {
				all = append(all, snapshotQuery{Name: "probe", SQL: source.ProbeSQL})
			}
			for _, query := range all {
				lower := strings.ToLower(query.SQL)
				if !strings.HasPrefix(strings.TrimSpace(lower), "select") &&
					!strings.HasPrefix(strings.TrimSpace(lower), "with") {
					t.Fatalf("%s/%s/%s is not a read-only query", kind, source.Name, query.Name)
				}
				for _, bad := range forbidden {
					if strings.Contains(lower, bad) {
						t.Fatalf("%s/%s/%s contains %q", kind, source.Name, query.Name, bad)
					}
				}
			}
		}
	}
}

// TestSnapshotProbeExistsButUnreadable：对象存在但当前账号读不到（openGauss 的
// dbe_perf 需 monadmin）→ 该源判不可用、链路降级，而不是硬报错。
// 这是 2026-08-13 CI 首次接入真 openGauss 时暴露的缺陷。
func TestSnapshotProbeExistsButUnreadable(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{
		responses: map[string][]map[string]string{
			// 两个源的"存在性"都成立……
			"pg_extension":         {{"?column?": "1"}},
			"nspname = 'dbe_perf'": {{"?column?": "1"}},
		},
		failOn: map[string]error{
			// ……但两个源的可读性探测都因权限失败。
			"FROM pg_stat_statements WHERE false":         errors.New("permission denied"),
			"FROM dbe_perf.summary_statement WHERE false": errors.New("permission denied for schema dbe_perf"),
		},
	}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("unreadable source must degrade, not fail: %v", err)
	}
	if !snap.CapabilityMissing || snap.Source != "" {
		t.Fatalf("snapshot = %+v, want CapabilityMissing", snap)
	}
	// 可读性不通过时，绝不该再去跑该源的采集 SQL。
	for _, sql := range q.executed {
		if strings.Contains(sql, "ORDER BY total_elapse_time") ||
			strings.Contains(sql, "ORDER BY s.total_exec_time") {
			t.Fatalf("collect query ran despite failed read check: %q", sql)
		}
	}
}

// TestSnapshotProbeReadableSourceStillCollects：可读性探测通过（零行也算通过，
// 因为判据是"不报错"而非"有行"）时正常采集。
func TestSnapshotProbeReadableSourceStillCollects(t *testing.T) {
	t.Parallel()
	q := &fakeRowQuerier{responses: map[string][]map[string]string{
		"pg_extension":                        {{"?column?": "1"}},
		"FROM pg_stat_statements WHERE false": {}, // 零行 = 可读且当前为空
		"pg_stat_statements s": {
			{"query_id": "1", "text": "SELECT $1", "calls": "2", "total_ms": "4"},
		},
	}}
	probe := SnapshotProbe{DatasourceID: "ds1", EngineFamily: "postgres"}

	snap, err := probe.Collect(context.Background(), q, SnapshotKindSlowlog)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if snap.Source != "pg_stat_statements" || len(snap.SlowQueries) != 1 {
		t.Fatalf("snapshot = source %q, %d entries", snap.Source, len(snap.SlowQueries))
	}
}
