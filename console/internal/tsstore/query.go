package tsstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// Point 是一条时间序列上的一个点。查询面统一返回聚合值——即使命中原始层，
// 也按 bucket 聚合，让调用方不必关心自己命中了哪一层。
type Point struct {
	At      time.Time `json:"at"`
	Avg     float64   `json:"avg"`
	Min     float64   `json:"min"`
	Max     float64   `json:"max"`
	Last    float64   `json:"last"`
	Samples int64     `json:"samples"`
}

// RankedEntity 是 Top N 查询的一行：实体 + 该 series 在窗口内的合计。
type RankedEntity struct {
	EntityID    string    `json:"entity_id"`
	Label       string    `json:"label"`
	NativeID    string    `json:"native_id,omitempty"`
	Total       float64   `json:"total"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// SnapshotMeta 是快照的元信息（不含 payload——列表场景不该拖着 512KB 走）。
type SnapshotMeta struct {
	ID                string     `json:"id"`
	Kind              string     `json:"kind"`
	Source            string     `json:"source"`
	CapabilityMissing bool       `json:"capability_missing"`
	Truncated         bool       `json:"truncated"`
	ContentHash       string     `json:"content_hash"`
	CollectedAt       time.Time  `json:"collected_at"`
	CreatedAt         time.Time  `json:"created_at"`
	SupersededAt      *time.Time `json:"superseded_at,omitempty"`
}

// SnapshotWithPayload 是元信息 + 内容。
type SnapshotWithPayload struct {
	SnapshotMeta
	Tables  []metrics.TableInfo   `json:"tables,omitempty"`
	Configs []metrics.ConfigEntry `json:"configs,omitempty"`
}

// 选层阈值：与 0004 迁移里的保留期一一对应（raw 14d / 5m 90d / 1h 400d）。
// 超出某层保留期的查询窗口必须落到更粗的层，否则会静默漏掉早于保留期的数据——
// 那种"图突然从某天开始才有线"的现象最难排查。
const (
	rawRetention = 14 * 24 * time.Hour
	fiveMinRet   = 90 * 24 * time.Hour
)

// seriesLayer 描述一层可查的数据。
type seriesLayer struct {
	relation      string // 视图名
	timeCol       string
	preAggregated bool // 连续聚合层已有 avg/min/max/last/count 列
}

// layerFor 按查询窗口选层。窗口起点越早，越必须走粗粒度层。
//
// 只看窗口起点不看 step：即使调用方要 1 秒粒度，30 天前的原始点也已被保留策略删了，
// 给不出来。选层是"数据还在不在"的问题，不是"想要多细"的问题。
func layerFor(from time.Time, now time.Time) seriesLayer {
	age := now.Sub(from)
	switch {
	case age <= rawRetention:
		return seriesLayer{relation: "collected.series", timeCol: "at"}
	case age <= fiveMinRet:
		return seriesLayer{relation: "collected.series_5m", timeCol: "bucket", preAggregated: true}
	default:
		return seriesLayer{relation: "collected.series_1h", timeCol: "bucket", preAggregated: true}
	}
}

// SeriesRange 查一条 series 在窗口内的曲线，按 step 分桶。
// entityID 为空串表示无实体维度的指标。
func (s *Store) SeriesRange(ctx context.Context, datasourceID, seriesName, entityID string,
	from, to time.Time, step time.Duration) (_ []Point, err error) {
	// 闭包而非 defer observeQuery(ctx, start, err)：defer 的实参在**注册时**求值，
	// 那样记下的永远是 nil，错误率指标会恒为 0——一种看着有监控其实没有的失败。
	start := time.Now()
	defer func() { observeQuery(ctx, start, err) }()

	if step <= 0 {
		return nil, apierror.Wrap(apierror.CodeTimeseriesQueryFailed,
			fmt.Errorf("step must be positive, got %v", step))
	}
	layer := layerFor(from, time.Now())

	// 原始层按 value 现算；聚合层从已物化的列再卷一次（加权平均，防"平均的平均"失真）。
	var selectList string
	if layer.preAggregated {
		selectList = `avg_value * sample_count AS w, min_value, max_value, last_value, sample_count`
	} else {
		selectList = `value AS w, value AS min_value, value AS max_value, value AS last_value, 1::bigint AS sample_count`
	}
	// #nosec G201 —— relation/timeCol 来自 layerFor 的闭集常量，非用户输入。
	sql := fmt.Sprintf(`
		WITH src AS (
			SELECT %[2]s AS t, %[1]s
			  FROM %[3]s
			 WHERE datasource_id = $1 AND series_name = $2 AND entity_id = $3
			   AND %[2]s >= $4 AND %[2]s < $5
		)
		SELECT time_bucket($6::interval, t) AS bucket,
		       sum(w) / NULLIF(sum(sample_count), 0) AS avg_value,
		       min(min_value), max(max_value),
		       last(last_value, t), sum(sample_count)
		  FROM src GROUP BY 1 ORDER BY 1`, selectList, layer.timeCol, layer.relation)

	var points []Point
	err = s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, datasourceID, seriesName, entityID, from, to, step)
		if err != nil {
			return fmt.Errorf("query %s: %w", layer.relation, err)
		}
		defer rows.Close()
		for rows.Next() {
			var p Point
			if err := rows.Scan(&p.At, &p.Avg, &p.Min, &p.Max, &p.Last, &p.Samples); err != nil {
				return fmt.Errorf("scan point: %w", err)
			}
			points = append(points, p)
		}
		return rows.Err()
	})
	if err != nil {
		err = apierror.Wrap(apierror.CodeTimeseriesQueryFailed, err)
		return nil, err
	}
	return points, nil
}

// TopEntities 按某条 series 在窗口内的合计排序取前 N，带出实体标签。
// 慢查询分析（spec-1.11）的主查询路径。
func (s *Store) TopEntities(ctx context.Context, datasourceID, seriesName string,
	from, to time.Time, n int) (_ []RankedEntity, err error) {
	start := time.Now()
	defer func() { observeQuery(ctx, start, err) }()

	if n <= 0 {
		n = 10
	}
	// 先在读数上聚合取前 N，再 join 字典——反过来会把窗口内全部读数行都带上字典
	// 再聚合，实测慢 25 倍以上（0.2ms → 5.3ms）。
	const sql = `
		WITH top AS (
			SELECT entity_id, sum(value) AS total
			  FROM collected.series
			 WHERE datasource_id = $1 AND series_name = $2 AND at >= $3 AND at < $4
			   AND entity_id <> ''
			 GROUP BY entity_id ORDER BY total DESC LIMIT $5
		)
		SELECT t.entity_id, COALESCE(e.label, ''), COALESCE(e.native_id, ''), t.total,
		       COALESCE(e.first_seen_at, 'epoch'::timestamptz),
		       COALESCE(e.last_seen_at, 'epoch'::timestamptz)
		  FROM top t
		  LEFT JOIN collected.entities e
		         ON e.datasource_id = $1 AND e.entity_kind = $6 AND e.entity_id = t.entity_id
		 ORDER BY t.total DESC`

	var out []RankedEntity
	err = s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, datasourceID, seriesName, from, to, n, metrics.EntityKindQuery)
		if err != nil {
			return fmt.Errorf("query top entities: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e RankedEntity
			if err := rows.Scan(&e.EntityID, &e.Label, &e.NativeID, &e.Total,
				&e.FirstSeenAt, &e.LastSeenAt); err != nil {
				return fmt.Errorf("scan ranked entity: %w", err)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		err = apierror.Wrap(apierror.CodeTimeseriesQueryFailed, err)
		return nil, err
	}
	return out, nil
}

// LatestSnapshot 取某数据源某 kind 的当前版本（含 payload）。
// 无快照时返回 (nil, nil)——"还没采到"是正常状态，不是错误。
func (s *Store) LatestSnapshot(ctx context.Context, datasourceID, kind string) (_ *SnapshotWithPayload, err error) {
	start := time.Now()
	defer func() { observeQuery(ctx, start, err) }()

	var out *SnapshotWithPayload
	err = s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var meta SnapshotMeta
		var payload []byte
		err := tx.QueryRow(ctx, `SELECT id::text, kind, source, capability_missing, truncated,
				content_hash, collected_at, created_at, payload
			FROM collected.snapshots
			WHERE datasource_id = $1 AND kind = $2 AND superseded_at IS NULL`,
			datasourceID, kind).Scan(&meta.ID, &meta.Kind, &meta.Source,
			&meta.CapabilityMissing, &meta.Truncated, &meta.ContentHash,
			&meta.CollectedAt, &meta.CreatedAt, &payload)
		if errIsNoRows(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load latest snapshot: %w", err)
		}
		snap := SnapshotWithPayload{SnapshotMeta: meta}
		if err := json.Unmarshal(payload, &snap); err != nil {
			return fmt.Errorf("decode snapshot payload %s: %w", meta.ID, err)
		}
		out = &snap
		return nil
	})
	if err != nil {
		err = apierror.Wrap(apierror.CodeTimeseriesQueryFailed, err)
		return nil, err
	}
	return out, nil
}

// SnapshotHistory 列出版本链（新→旧，不含 payload）。
// "这个库最近改过什么"从这里起步：拿到两个版本 ID 再各自取 payload 做 diff。
func (s *Store) SnapshotHistory(ctx context.Context, datasourceID, kind string, limit int) (_ []SnapshotMeta, err error) {
	start := time.Now()
	defer func() { observeQuery(ctx, start, err) }()

	if limit <= 0 {
		limit = 20
	}
	var out []SnapshotMeta
	err = s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, kind, source, capability_missing, truncated,
				content_hash, collected_at, created_at, superseded_at
			FROM collected.snapshots
			WHERE datasource_id = $1 AND kind = $2
			ORDER BY created_at DESC LIMIT $3`, datasourceID, kind, limit)
		if err != nil {
			return fmt.Errorf("query snapshot history: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m SnapshotMeta
			if err := rows.Scan(&m.ID, &m.Kind, &m.Source, &m.CapabilityMissing,
				&m.Truncated, &m.ContentHash, &m.CollectedAt, &m.CreatedAt,
				&m.SupersededAt); err != nil {
				return fmt.Errorf("scan snapshot meta: %w", err)
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		err = apierror.Wrap(apierror.CodeTimeseriesQueryFailed, err)
		return nil, err
	}
	return out, nil
}
