package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// RowQuerier 是快照探针需要的最小只读行查询面（spec-1.4 §2.3）。既有 Querier
// （指标单值）不变；两通道的适配器同时实现两个接口，探针代码仍两通道共享。
type RowQuerier interface {
	// QueryRows 只读执行一条目录 SQL，返回行（列名 → 字符串值；NULL 为空串）。
	// maxRows 是防御性上限，超出即截断（返回的行数不超过 maxRows）。
	QueryRows(ctx context.Context, sql string, maxRows int) ([]map[string]string, error)
}

// ErrUnsupportedKind 表示采集类型不在白名单内（AD-9 显式拒绝面）。
var ErrUnsupportedKind = errors.New("metrics: unsupported snapshot kind")

// ErrNoSnapshotCatalog 表示该引擎族无此 kind 的 Stage-1 快照目录。
var ErrNoSnapshotCatalog = errors.New("metrics: no snapshot catalog for engine family")

// SnapshotProbe 是无状态、只读的快照探针（spec-1.4 §2.3）。
type SnapshotProbe struct {
	DatasourceID string
	EngineFamily string
}

// Collect 按 kind 的源候选链依次探测并采集：首个"能力可用且采集成功"的源产出快照；
// 全部候选都不可用即 CapabilityMissing 降级（成功路径，不是错误，调度不退避）；
// 有候选可用但全部采集失败才返回错误。
func (p SnapshotProbe) Collect(ctx context.Context, q RowQuerier, kind string) (Snapshot, error) {
	if !ValidSnapshotKind(kind) {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrUnsupportedKind, kind)
	}
	sources := snapshotSourcesFor(p.EngineFamily, kind)
	if sources == nil {
		return Snapshot{}, fmt.Errorf("%w: %s/%s", ErrNoSnapshotCatalog, p.EngineFamily, kind)
	}

	snap := Snapshot{
		DatasourceID:   p.DatasourceID,
		EngineFamily:   p.EngineFamily,
		Kind:           kind,
		CatalogVersion: CatalogVersion,
		CollectedAt:    nowFunc().UTC(),
	}

	var collectErrs []error
	for _, source := range sources {
		available, err := sourceAvailable(ctx, q, source)
		if err != nil {
			collectErrs = append(collectErrs, err)
			continue
		}
		if !available {
			continue
		}
		rows, err := runSourceQueries(ctx, q, source)
		if err != nil {
			// 源已探测可用却采集失败（如引擎方言列名不符）——试下一个候选。
			collectErrs = append(collectErrs, fmt.Errorf("source %s: %w", source.Name, err))
			continue
		}
		snap.Source = source.Name
		fillSnapshot(&snap, kind, rows)
		enforceSnapshotSize(&snap)
		return snap, nil
	}

	if len(collectErrs) > 0 {
		return Snapshot{}, fmt.Errorf("metrics: %s snapshot failed: %w", kind, errors.Join(collectErrs...))
	}
	// 无候选可用：结构化降级，供上层提示"开启 pg_stat_statements / dbe_perf"。
	snap.CapabilityMissing = true
	return snap, nil
}

// sourceAvailable 判定源是否可用：先判存在（ProbeSQL 返回行），再判可读
// （ReadCheckSQL 不报错）。任一不成立即"该源不可用"，让链路走候选链或降级，
// 而不是把权限问题冒充成采集失败。
func sourceAvailable(ctx context.Context, q RowQuerier, source snapshotSource) (bool, error) {
	if source.ProbeSQL != "" {
		rows, err := q.QueryRows(ctx, source.ProbeSQL, 1)
		// 探测本身失败（视图不存在/无权限）同样按不可用处理。
		if err != nil || len(rows) == 0 {
			return false, nil
		}
	}
	if source.ReadCheckSQL != "" {
		if _, err := q.QueryRows(ctx, source.ReadCheckSQL, 1); err != nil {
			return false, nil
		}
	}
	return true, nil
}

// runSourceQueries 依次执行源内每条查询，按查询名归集结果。
func runSourceQueries(ctx context.Context, q RowQuerier, source snapshotSource) (map[string][]map[string]string, error) {
	out := make(map[string][]map[string]string, len(source.Queries))
	for _, query := range source.Queries {
		rows, err := q.QueryRows(ctx, query.SQL, query.MaxRows)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", query.Name, err)
		}
		out[query.Name] = rows
	}
	return out, nil
}

// fillSnapshot 把原始行解析成该 kind 的强类型条目。
func fillSnapshot(snap *Snapshot, kind string, rows map[string][]map[string]string) {
	switch kind {
	case SnapshotKindSlowlog:
		snap.SlowQueries, snap.Truncated = parseSlowQueries(rows["slow_queries"])
	case SnapshotKindSchema:
		snap.Tables, snap.Truncated = parseTables(rows)
	case SnapshotKindConfig:
		snap.Configs = parseConfigs(rows["configs"])
	}
}

// parseSlowQueries 解析慢查询统计行，文本超长即截断（AD-3 尺寸有界）。
func parseSlowQueries(rows []map[string]string) ([]SlowQueryEntry, bool) {
	if len(rows) > SlowlogTopN {
		rows = rows[:SlowlogTopN]
	}
	truncated := false
	entries := make([]SlowQueryEntry, 0, len(rows))
	for _, row := range rows {
		text, cut := truncateText(row["text"])
		if cut {
			truncated = true
		}
		entries = append(entries, SlowQueryEntry{
			QueryID:   row["query_id"],
			Text:      text,
			Truncated: cut,
			Calls:     parseInt(row["calls"]),
			TotalMs:   parseFloat(row["total_ms"]),
			MeanMs:    parseFloat(row["mean_ms"]),
			MaxMs:     parseFloat(row["max_ms"]),
			Rows:      parseInt(row["rows"]),
			Database:  row["database"],
		})
	}
	return entries, truncated
}

// parseTables 组装表结构快照：先建表，再按 (schema, name) 挂列与索引。
func parseTables(rows map[string][]map[string]string) ([]TableInfo, bool) {
	tableRows := rows["tables"]
	truncated := false
	if len(tableRows) > SchemaMaxTables {
		tableRows = tableRows[:SchemaMaxTables]
		truncated = true
	}

	tables := make([]TableInfo, 0, len(tableRows))
	index := make(map[string]int, len(tableRows))
	for _, row := range tableRows {
		key := tableKey(row["schema"], row["name"])
		index[key] = len(tables)
		tables = append(tables, TableInfo{
			Schema:      row["schema"],
			Name:        row["name"],
			SizeBytes:   parseInt(row["size_bytes"]),
			RowEstimate: parseInt(row["row_estimate"]),
		})
	}

	for _, row := range rows["columns"] {
		pos, ok := index[tableKey(row["schema"], row["name"])]
		if !ok {
			continue // 该表未进入 TopN，丢弃其列
		}
		tables[pos].Columns = append(tables[pos].Columns, ColumnInfo{
			Name:     row["column_name"],
			DataType: row["data_type"],
			Nullable: row["nullable"] == "true",
		})
	}

	for _, row := range rows["indexes"] {
		pos, ok := index[tableKey(row["schema"], row["name"])]
		if !ok {
			continue
		}
		tables[pos].Indexes = append(tables[pos].Indexes, IndexInfo{
			Name:     row["index_name"],
			IsUnique: row["is_unique"] == "true",
			Columns:  parseIndexColumns(row["index_def"]),
		})
	}
	return tables, truncated
}

// parseConfigs 解析实例配置行。
func parseConfigs(rows []map[string]string) []ConfigEntry {
	entries := make([]ConfigEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, ConfigEntry{
			Name:   row["name"],
			Value:  row["value"],
			Unit:   row["unit"],
			Source: row["source"],
		})
	}
	return entries
}

// parseIndexColumns 从 pg_get_indexdef 的 DDL 里取出索引列清单。
// 用 DDL 而非 indkey 展开，是为了兼容 openGauss（其 PG 9.2 血统缺 LATERAL /
// WITH ORDINALITY）。表达式索引的表达式原样保留——那是结构元数据，不是行数据。
func parseIndexColumns(indexDef string) []string {
	open := strings.Index(indexDef, "(")
	if open < 0 {
		return nil
	}
	depth := 0
	var current strings.Builder
	var columns []string
	for _, r := range indexDef[open:] {
		switch {
		case r == '(':
			depth++
			if depth == 1 {
				continue // 最外层左括号不入内容
			}
		case r == ')':
			depth--
			if depth == 0 {
				if col := strings.TrimSpace(current.String()); col != "" {
					columns = append(columns, col)
				}
				return columns
			}
		case r == ',' && depth == 1:
			if col := strings.TrimSpace(current.String()); col != "" {
				columns = append(columns, col)
			}
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	return columns
}

func tableKey(schema, name string) string { return schema + "\x00" + name }

// enforceSnapshotSize 把序列化体积压到 SnapshotMaxBytes 以内：超限即从尾部
// （最不重要的一端——慢查询按耗时降序、表按大小降序）成批丢弃并标记截断。
func enforceSnapshotSize(snap *Snapshot) {
	for snapshotSize(snap) > SnapshotMaxBytes {
		switch {
		case len(snap.SlowQueries) > 0:
			snap.SlowQueries = snap.SlowQueries[:dropTail(len(snap.SlowQueries))]
		case len(snap.Tables) > 0:
			snap.Tables = snap.Tables[:dropTail(len(snap.Tables))]
		case len(snap.Configs) > 0:
			snap.Configs = snap.Configs[:dropTail(len(snap.Configs))]
		default:
			return // 空快照仍超限：无可再丢，交由上层报错
		}
		snap.Truncated = true
	}
}

// dropTail 返回丢掉尾部约 10%（至少一条）后的长度。
func dropTail(n int) int {
	drop := n / 10
	if drop < 1 {
		drop = 1
	}
	return n - drop
}

// snapshotSize 返回快照 JSON 序列化后的字节数；序列化失败按超限处理（保守）。
func snapshotSize(snap *Snapshot) int {
	buf, err := json.Marshal(snap)
	if err != nil {
		return SnapshotMaxBytes + 1
	}
	return len(buf)
}

func parseInt(s string) int64 {
	// PG 的 reltuples 等列可能是 "123.0" 形态，先按浮点解析再取整。
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
