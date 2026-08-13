package metrics

import "time"

// 快照采集（spec-1.4）：与指标（单值时序）并列的第二类采集产物——行结构数据。
// 三类 kind 共用一个 Snapshot 信封，各自填充自己的强类型条目（§8 Q2：强类型而非
// 通用 rows，让字段白名单在类型层生效、下游契约稳定）。

// 快照类型（受控白名单——平台与连接器双侧据此拒绝未知类型，AD-9）。
const (
	SnapshotKindSlowlog = "slowlog"
	SnapshotKindSchema  = "schema"
	SnapshotKindConfig  = "config"
)

// SnapshotKinds 是全部合法 kind，顺序稳定（调度与测试遍历用）。
var SnapshotKinds = []string{SnapshotKindSlowlog, SnapshotKindSchema, SnapshotKindConfig}

// ValidSnapshotKind 报告 kind 是否在白名单内。
func ValidSnapshotKind(kind string) bool {
	for _, k := range SnapshotKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// 采集上限（受控口径，spec-1.4 §2.1；变更须修订 spec）。
const (
	// SlowlogTopN 是慢查询按累计耗时降序保留的条目数。
	SlowlogTopN = 50
	// SchemaMaxTables 是表结构快照按表大小降序保留的表数。
	SchemaMaxTables = 500
	// QueryTextMaxLen 是单条慢查询规范化文本的字符上限。
	QueryTextMaxLen = 2048
	// SnapshotMaxBytes 是快照序列化后的字节上限（远低于会话帧限）。
	SnapshotMaxBytes = 512 * 1024
)

// Snapshot 是一次快照采集的产物（AD-3：聚合系统视图/目录，零业务表行数据）。
type Snapshot struct {
	DatasourceID   string    `json:"datasource_id"`
	EngineFamily   string    `json:"engine_family"`
	Kind           string    `json:"kind"`
	CatalogVersion int       `json:"catalog_version"`
	CollectedAt    time.Time `json:"collected_at"`

	// CapabilityMissing 表示数据源缺该 kind 所需能力（如未装 pg_stat_statements）。
	// 这是**成功路径的结构化降级**，不是错误——上层据此提示开启，调度不进退避。
	CapabilityMissing bool `json:"capability_missing,omitempty"`
	// Source 记实际命中的采集源（如 "pg_stat_statements"），能力缺失时为空。
	Source string `json:"source,omitempty"`
	// Truncated 表示任一上限触发了截断。
	Truncated bool `json:"truncated,omitempty"`

	SlowQueries []SlowQueryEntry `json:"slow_queries,omitempty"` // kind=slowlog
	Tables      []TableInfo      `json:"tables,omitempty"`       // kind=schema
	Configs     []ConfigEntry    `json:"configs,omitempty"`      // kind=config
}

// SlowQueryEntry 是一条规范化慢查询统计。
//
// Text 只取自统计视图的**规范化**语句（字面量已被服务端替换为 $N/? 占位符）——
// 绝不取 pg_stat_activity.query 那种带字面量的原始 SQL（spec-1.4 §3，AD-3 编译期防线）。
type SlowQueryEntry struct {
	QueryID   string  `json:"query_id"`
	Text      string  `json:"text"`
	Truncated bool    `json:"truncated,omitempty"`
	Calls     int64   `json:"calls"`
	TotalMs   float64 `json:"total_ms"`
	MeanMs    float64 `json:"mean_ms"`
	MaxMs     float64 `json:"max_ms"`
	Rows      int64   `json:"rows"`
	Database  string  `json:"database,omitempty"`
}

// TableInfo 是一张表的结构与规模快照。
type TableInfo struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name"`
	Columns     []ColumnInfo `json:"columns,omitempty"`
	Indexes     []IndexInfo  `json:"indexes,omitempty"`
	SizeBytes   int64        `json:"size_bytes"`
	RowEstimate int64        `json:"row_estimate"`
}

// ColumnInfo 是一列的定义（无样本值——只要结构不要数据）。
type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

// IndexInfo 是一个索引的定义。
type IndexInfo struct {
	Name     string   `json:"name"`
	IsUnique bool     `json:"is_unique"`
	Columns  []string `json:"columns,omitempty"`
}

// ConfigEntry 是一项实例配置（pg_settings 一行）。
type ConfigEntry struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Unit   string `json:"unit,omitempty"`
	Source string `json:"source,omitempty"`
}

// truncateText 把文本截到 QueryTextMaxLen 个字符（按 rune，不切碎多字节），
// 返回截断后文本与是否发生截断。
func truncateText(s string) (string, bool) {
	runes := []rune(s)
	if len(runes) <= QueryTextMaxLen {
		return s, false
	}
	return string(runes[:QueryTextMaxLen]), true
}
