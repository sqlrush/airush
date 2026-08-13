package directconn

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sqlrush/airush/libs/metrics"
)

// MetricsQuerier 返回绑定某 datasource 的 metrics.Querier（spec-1.3 Direct 通道）。
// 采集经该 datasource 的直连池执行——探针代码通道无关，Direct 只提供此 Querier。
func (m *Manager) MetricsQuerier(datasourceID string) metrics.Querier {
	return &directQuerier{mgr: m, datasourceID: datasourceID}
}

// 编译期断言：directQuerier 满足通道无关探针接口（spec-1.3 T4 编译期部分）。
var _ metrics.Querier = (*directQuerier)(nil)

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
