package tsstore

import (
	"testing"
	"time"
)

// TestLayerForBoundaries spec-1.5 T17（纯函数部分）：选层阈值必须与 0004 迁移里的
// 三个保留期严丝合缝。边界写错一天不会报错，只会让恰好落在边界的窗口查出空图。
func TestLayerForBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"刚刚", time.Minute, "collected.series"},
		{"原始层保留期上沿", rawRetention, "collected.series"},
		{"刚过原始层保留期", rawRetention + time.Second, "collected.series_5m"},
		{"5m 层保留期上沿", fiveMinRet, "collected.series_5m"},
		{"刚过 5m 层保留期", fiveMinRet + time.Second, "collected.series_1h"},
		{"一年前", 365 * 24 * time.Hour, "collected.series_1h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := layerFor(now.Add(-c.age), now)
			if got.relation != c.want {
				t.Fatalf("age=%v 选到 %s, want %s", c.age, got.relation, c.want)
			}
			// 聚合层必须声明 preAggregated，否则读路径会拿 value 列现算——那列不存在。
			if wantPre := c.want != "collected.series"; got.preAggregated != wantPre {
				t.Fatalf("%s: preAggregated=%v, want %v", c.want, got.preAggregated, wantPre)
			}
		})
	}
}
