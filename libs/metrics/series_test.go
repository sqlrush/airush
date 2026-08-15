package metrics

import (
	"errors"
	"strings"
	"testing"
)

// TestSeriesTwoTierNaming spec-1.5 T12：两层命名成立——规范层 db.* 与引擎特有层
// pg.* 都在注册表里，且 unit 由目录唯一决定（不由调用方传入）。
func TestSeriesTwoTierNaming(t *testing.T) {
	cases := []struct {
		name     string
		wantUnit string
	}{
		{"db.connections.active", UnitCount},
		{"db.cache.hit_ratio", UnitRatio},
		{"db.storage.size_bytes", UnitBytes},
		{"db.replication.lag_seconds", UnitSeconds},
		{"pg.replication.lag_bytes", UnitBytes}, // 引擎特有层
		{SeriesSlowlogTotalSec, UnitSeconds},
	}
	for _, tc := range cases {
		entry, ok := LookupSeries(tc.name)
		if !ok {
			t.Errorf("%s 不在 series 注册表里", tc.name)
			continue
		}
		if entry.Unit != tc.wantUnit {
			t.Errorf("%s unit = %q, want %q", tc.name, entry.Unit, tc.wantUnit)
		}
	}
}

// TestNoStrayEngineSpecificCanonicals spec-1.5 DoD：规范类概念不得停留在引擎前缀上。
// 白名单外的 pg.* 出现即认为是"该上升为 db.* 却没上升"，改名时容易漏。
func TestNoStrayEngineSpecificCanonicals(t *testing.T) {
	// 经论证确属 PG 族特有、别的引擎无对等概念的条目。
	allowed := map[string]bool{"pg.replication.lag_bytes": true}
	for _, entry := range PostgresCatalog {
		if strings.HasPrefix(entry.Name, "db.") {
			continue
		}
		if !allowed[entry.Name] {
			t.Errorf("%s 用了引擎前缀但不在特有白名单里——"+
				"要么改成 db.* 规范名，要么在 allowed 里论证它为何是 PG 独有", entry.Name)
		}
	}
}

// TestValidateSeriesEntity spec-1.5 T6：AD-3 防线——实体维度必须目录声明，
// 两个方向都要拒（有 entity 无声明、有声明无 entity）。
func TestValidateSeriesEntity(t *testing.T) {
	cases := []struct {
		desc    string
		series  string
		entity  string
		wantErr error
	}{
		{"无实体指标不带 entity", "db.connections.active", "", nil},
		{"慢查询 series 带 entity", SeriesSlowlogTotalSec, "abc123", nil},
		{"未声明的 series", "db.made.up", "", ErrUndeclaredSeries},
		{"无实体指标却带 entity", "db.connections.active", "abc123", ErrUndeclaredEntity},
		{"实体 series 却无 entity", SeriesSlowlogCalls, "", ErrUndeclaredEntity},
	}
	for _, tc := range cases {
		err := ValidateSeriesEntity(tc.series, tc.entity)
		switch {
		case tc.wantErr == nil && err != nil:
			t.Errorf("%s: 意外报错 %v", tc.desc, err)
		case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
			t.Errorf("%s: err = %v, want %v", tc.desc, err, tc.wantErr)
		}
	}
}

// TestEntityIDStability spec-1.5 §8 Q4：同一规范化文本恒得同一 ID（跨实例可比），
// 不同文本得不同 ID。这是"主备两个实例上是同一个实体"的基础。
func TestEntityIDStability(t *testing.T) {
	const sql = `SELECT * FROM orders WHERE id = $1`
	a, b := EntityIDFor(sql), EntityIDFor(sql)
	if a != b {
		t.Fatalf("同文本两次得 %q / %q，实体身份不稳定", a, b)
	}
	if len(a) != entityIDLen {
		t.Fatalf("实体 ID 长度 = %d, want %d", len(a), entityIDLen)
	}
	if other := EntityIDFor(sql + " "); other == a {
		t.Fatal("不同文本得到相同 ID")
	}
}

// TestSlowQuerySeriesValues spec-1.5 T2：一条慢查询展开成 5 条 series，
// 且耗时类完成毫秒→秒换算（规范层单位统一的唯一换算点）。
func TestSlowQuerySeriesValues(t *testing.T) {
	entry := SlowQueryEntry{
		Calls: 10, TotalMs: 2500, MeanMs: 250, MaxMs: 900, Rows: 42,
	}
	values := SlowQuerySeriesValues(entry)
	if len(values) != len(SlowlogSeries) {
		t.Fatalf("展开 %d 条，SlowlogSeries 声明 %d 条", len(values), len(SlowlogSeries))
	}

	want := map[string]float64{
		SeriesSlowlogCalls:    10,
		SeriesSlowlogTotalSec: 2.5,
		SeriesSlowlogMeanSec:  0.25,
		SeriesSlowlogMaxSec:   0.9,
		SeriesSlowlogRows:     42,
	}
	for _, v := range values {
		w, ok := want[v.Name]
		if !ok {
			t.Errorf("展开出未预期的 series %s", v.Name)
			continue
		}
		if v.Value != w {
			t.Errorf("%s = %v, want %v", v.Name, v.Value, w)
		}
		// 每条展开出来的 series 都必须能过 AD-3 校验，否则 sink 会在运行期拒它。
		if err := ValidateSeriesEntity(v.Name, "e1"); err != nil {
			t.Errorf("%s 未通过实体校验: %v", v.Name, err)
		}
	}
}
