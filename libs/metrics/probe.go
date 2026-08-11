package metrics

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Querier 是探针需要的最小只读查询面。Direct 通道由 directconn 连接池适配、
// Connector 通道由客户侧连接适配——两通道只提供 Querier，探针代码共享（防漂移）。
type Querier interface {
	// QueryMetricValue 执行一条只读 SQL，返回单个 value 列的数值；无行返回 (0,false,nil)。
	QueryMetricValue(ctx context.Context, sql string) (value float64, present bool, err error)
}

// nowFunc 可被测试覆盖以固定采集时刻。
var nowFunc = time.Now

// Probe 是无状态、只读的指标探针（spec-1.3 §2.2）。
type Probe struct {
	DatasourceID string
	EngineFamily string
}

// ErrNoCatalog 表示该引擎族无 Stage-1 指标目录。
var ErrNoCatalog = errors.New("metrics: no catalog for engine family")

// Collect 遍历引擎族目录、逐条只读执行、解析为 Batch。单条 SQL 失败/无值记 partial
// 缺采（该指标缺失），不中断整批；Labels 经白名单构造（AD-3，spec-1.3 §3）。
func (p Probe) Collect(ctx context.Context, q Querier) (Batch, error) {
	catalog := CatalogFor(p.EngineFamily)
	if catalog == nil {
		return Batch{}, fmt.Errorf("%w: %s", ErrNoCatalog, p.EngineFamily)
	}

	baseLabels, _ := sanitizeLabels(map[string]string{"datasource_id": p.DatasourceID})
	at := nowFunc().UTC()

	batch := Batch{
		DatasourceID:   p.DatasourceID,
		EngineFamily:   p.EngineFamily,
		CatalogVersion: CatalogVersion,
		CollectedAt:    at,
	}
	for _, entry := range catalog {
		value, present, err := q.QueryMetricValue(ctx, entry.SQL)
		if err != nil || !present {
			// 失败或无值（如复制延迟在主库）→ 缺采，不进 batch，不算整批失败
			batch.Partial = true
			batch.Missing = append(batch.Missing, entry.Name)
			continue
		}
		batch.Metrics = append(batch.Metrics, Metric{
			Name:   entry.Name,
			Value:  value,
			Unit:   entry.Unit,
			Labels: cloneLabels(baseLabels),
			At:     at,
		})
	}

	// 整批一条都没采到 → 视为采集失败（连接/权限普遍性问题）
	if len(batch.Metrics) == 0 {
		return Batch{}, errors.New("metrics: collected no metrics (connection or permission failure)")
	}
	return batch, nil
}

func cloneLabels(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
