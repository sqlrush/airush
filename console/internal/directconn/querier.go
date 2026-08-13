package directconn

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/sqlrush/airush/libs/metrics"
)

// MetricsQuerier 返回绑定某 datasource 的 metrics.Querier（spec-1.3 Direct 通道）。
// 采集经该 datasource 的直连池执行——探针代码通道无关，Direct 只提供此 Querier。
func (m *Manager) MetricsQuerier(datasourceID string) metrics.Querier {
	return &directQuerier{mgr: m, datasourceID: datasourceID}
}

// SnapshotQuerier 返回绑定某 datasource 的 metrics.RowQuerier（spec-1.4 Direct 通道）。
// 与 MetricsQuerier 同一实现、同一连接池，只是取行而非单值。
func (m *Manager) SnapshotQuerier(datasourceID string) metrics.RowQuerier {
	return &directQuerier{mgr: m, datasourceID: datasourceID}
}

// 编译期断言：directQuerier 同时满足指标（单值）与快照（行）两个探针接口
// （spec-1.3 T4 / spec-1.4 T7 的编译期部分）——两通道只提供 Querier，探针共享。
var (
	_ metrics.Querier    = (*directQuerier)(nil)
	_ metrics.RowQuerier = (*directQuerier)(nil)
)

type directQuerier struct {
	mgr          *Manager
	datasourceID string
}

// QueryMetricValue 只读执行一条指标 SQL，取单个 value 列。NULL 值 → present=false
// （如复制延迟在主库无值），由探针记 partial 缺采。
func (q *directQuerier) QueryMetricValue(ctx context.Context, query string) (float64, bool, error) {
	pool, err := q.mgr.poolFor(ctx, q.datasourceID)
	if err != nil {
		return 0, false, err
	}
	var v sql.NullFloat64
	if err := pool.QueryRow(ctx, query).Scan(&v); err != nil {
		return 0, false, fmt.Errorf("directconn: query metric: %w", err)
	}
	if !v.Valid {
		return 0, false, nil
	}
	return v.Float64, true, nil
}

// QueryRows 只读执行一条快照目录 SQL，把结果按 列名 → 字符串值 逐行返回
// （NULL 为空串）。maxRows 是防御性上限：达到即停止读取，剩余行丢弃。
//
// 值统一走字符串是有意为之：快照目录跨引擎方言取回的是异构标量（数值/文本/布尔/
// 时间戳），字符串是唯一无损且无需逐列类型表的中间形态，解析交给强类型的 kind
// 解析器（spec-1.4 §2.3）。
func (q *directQuerier) QueryRows(ctx context.Context, query string, maxRows int) ([]map[string]string, error) {
	pool, err := q.mgr.poolFor(ctx, q.datasourceID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("directconn: query rows: %w", err)
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = string(f.Name)
	}

	out := make([]map[string]string, 0, 16)
	for rows.Next() {
		if maxRows > 0 && len(out) >= maxRows {
			break
		}
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("directconn: scan row: %w", err)
		}
		row := make(map[string]string, len(names))
		for i, name := range names {
			if i < len(values) {
				row[name] = stringifyValue(values[i])
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directconn: read rows: %w", err)
	}
	return out, nil
}

// stringifyValue 把 pgx 解出的任意标量转成字符串；NULL → 空串。
func stringifyValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	case bool:
		if value {
			return "true"
		}
		return "false"
	case time.Time:
		return value.UTC().Format(time.RFC3339Nano)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(value), 'f', -1, 32)
	case int64:
		return strconv.FormatInt(value, 10)
	case int32:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	default:
		return fmt.Sprint(value)
	}
}
