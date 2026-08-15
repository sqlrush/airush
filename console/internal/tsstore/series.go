package tsstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// seriesRow 是待落库的一条读数（强类型 → 通用存储的中间形态）。
type seriesRow struct {
	datasourceID string
	seriesName   string
	entityID     string
	value        float64
	at           time.Time
}

// entityRow 是待 upsert 的一个实体。
type entityRow struct {
	datasourceID string
	kind         string
	id           string
	label        string
	nativeID     string
	seenAt       time.Time
}

// Publish 落一批指标（metrics.Sink）。
//
// 指标无实体维度，故 entity_id 恒为空串；校验仍走同一条 AD-3 防线——
// 若某天目录给指标加了实体却忘了声明，这里会显式报错而不是悄悄写进去。
func (s *Store) Publish(ctx context.Context, batch metrics.Batch) error {
	rows := make([]seriesRow, 0, len(batch.Metrics))
	for _, m := range batch.Metrics {
		if err := metrics.ValidateSeriesEntity(m.Name, ""); err != nil {
			return observeWrite(ctx, 0, apierror.Wrap(apierror.CodeTimeseriesUndeclaredSeries, err))
		}
		rows = append(rows, seriesRow{
			datasourceID: batch.DatasourceID,
			seriesName:   m.Name,
			value:        m.Value,
			at:           m.At,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	return observeWrite(ctx, len(rows), s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return s.insertSeries(ctx, tx, rows)
	}))
}

// publishSlowlog 把一份慢查询快照展开成实体 + 读数写入。
//
// 实体先于读数、同一事务：保证不出现"有读数无实体"的悬挂行（spec-1.5 §3.3）。
// 事务失败整批回滚——宁可这一轮采集全丢，也不留半份数据让下游算出错误的趋势。
func (s *Store) publishSlowlog(ctx context.Context, snap metrics.Snapshot) error {
	entities := make([]entityRow, 0, len(snap.SlowQueries))
	rows := make([]seriesRow, 0, len(snap.SlowQueries)*len(metrics.SlowlogSeries))

	for _, q := range snap.SlowQueries {
		// 实体身份取内容哈希而非引擎给的 ID（spec-1.5 §8 Q4）：
		// 同一条 SQL 在主备两个实例上必须是同一个实体，原生 ID 做不到。
		entityID := metrics.EntityIDFor(q.Text)
		entities = append(entities, entityRow{
			datasourceID: snap.DatasourceID,
			kind:         metrics.EntityKindQuery,
			id:           entityID,
			label:        q.Text,
			nativeID:     q.QueryID,
			seenAt:       snap.CollectedAt,
		})
		for _, v := range metrics.SlowQuerySeriesValues(q) {
			if err := metrics.ValidateSeriesEntity(v.Name, entityID); err != nil {
				return observeWrite(ctx, 0, apierror.Wrap(apierror.CodeTimeseriesUndeclaredSeries, err))
			}
			rows = append(rows, seriesRow{
				datasourceID: snap.DatasourceID,
				seriesName:   v.Name,
				entityID:     entityID,
				value:        v.Value,
				at:           snap.CollectedAt,
			})
		}
	}
	if len(rows) == 0 {
		return nil // 能力缺失或本轮无慢查询：不是错误
	}
	return observeWrite(ctx, len(rows), s.inTenantTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.upsertEntities(ctx, tx, entities); err != nil {
			return err
		}
		return s.insertSeries(ctx, tx, rows)
	}))
}

// insertSeries 分批写读数。经 collected.series 视图而非基表——
// 视图的 check_option 是 AD-10 等效形态的第四项门槛，绕过它写就等于自废武功。
func (s *Store) insertSeries(ctx context.Context, tx pgx.Tx, rows []seriesRow) error {
	tenantID, _ := tenancy.FromContext(ctx) // inTenantTx 已保证存在
	for start := 0; start < len(rows); start += s.batchMaxRows {
		end := min(start+s.batchMaxRows, len(rows))
		batch := &pgx.Batch{}
		for _, r := range rows[start:end] {
			batch.Queue(`INSERT INTO collected.series
				(tenant_id, datasource_id, series_name, entity_id, value, at)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				tenantID, r.datasourceID, r.seriesName, r.entityID, r.value, r.at)
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
				fmt.Errorf("insert series rows [%d,%d): %w", start, end, err))
		}
	}
	return nil
}

// upsertEntities 写实体字典：已存在则只推进 last_seen_at 与 label
// （SQL 文本可能因引擎升级换规范化写法；first_seen_at 永不回退——
// "这条慢 SQL 什么时候第一次出现"是这张表的核心价值）。
func (s *Store) upsertEntities(ctx context.Context, tx pgx.Tx, entities []entityRow) error {
	tenantID, _ := tenancy.FromContext(ctx) // inTenantTx 已保证存在
	batch := &pgx.Batch{}
	for _, e := range entities {
		batch.Queue(`INSERT INTO collected.entities
			(tenant_id, datasource_id, entity_kind, entity_id, label, native_id,
			 first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
			ON CONFLICT (tenant_id, datasource_id, entity_kind, entity_id) DO UPDATE
			SET last_seen_at = GREATEST(collected.entities.last_seen_at, EXCLUDED.last_seen_at),
			    label        = EXCLUDED.label,
			    native_id    = EXCLUDED.native_id`,
			tenantID, e.datasourceID, e.kind, e.id, e.label, e.nativeID, e.seenAt)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return apierror.Wrap(apierror.CodeTimeseriesWriteFailed,
			fmt.Errorf("upsert %d entities: %w", len(entities), err))
	}
	return nil
}
