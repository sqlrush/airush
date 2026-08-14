// Package tsstore 是采集数据的持久化落点与查询面（spec-1.5 D3/D4）。
//
// 它替换 spec-1.3/1.4 里验证链路用的内存 BufferSink，实现同样的 metrics.Sink 与
// metrics.SnapshotSink 接口——上游探针、网关、调度器一行都不用改。
//
// 强类型（Go 侧）→ 通用存储（DB 侧）的转换只发生在本包，是唯一一处。
// spec-1.4 §8 Q2 定的"强类型而非通用 rows"管的是 Go 侧与 API 契约，那部分完整保留；
// 存储层泛化是为了让表数固定住（spec-1.5 §2.2）。两者不冲突，前提是转换收口在一点。
package tsstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/metrics"
)

// Store 是本包的持久化句柄。
//
// 它自己开租户事务而不复用 repo.Store：repo 的查询函数面向控制面领域表，
// 而这里写的是 collected/tsdb 两个 schema，两者除了"都要 SET LOCAL 租户上下文"
// 之外没有共享面。复制的是那 4 行事务前奏，换来包边界干净。
type Store struct {
	pool *pgxpool.Pool
	// batchMaxRows 是单次写入的行数上限；超出分批（AIRUSH_TS_BATCH_MAX_ROWS）。
	batchMaxRows int
}

// 编译期断言：本包同时是指标与快照的落点，与 BufferSink 可互换。
var (
	_ metrics.Sink         = (*Store)(nil)
	_ metrics.SnapshotSink = (*Store)(nil)
)

// DefaultBatchMaxRows 是未配置时的单批行数上限。
const DefaultBatchMaxRows = 5000

// New 建池并 ping（启动期 fail fast，与 repo.New 同口径）。
func New(ctx context.Context, dbURL string, batchMaxRows int) (*Store, error) {
	if batchMaxRows <= 0 {
		batchMaxRows = DefaultBatchMaxRows
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("tsstore: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("tsstore: ping: %w", err)
	}
	return &Store{pool: pool, batchMaxRows: batchMaxRows}, nil
}

// NewWithPool 复用既有池（测试与同进程共享池场景）。
func NewWithPool(pool *pgxpool.Pool, batchMaxRows int) *Store {
	if batchMaxRows <= 0 {
		batchMaxRows = DefaultBatchMaxRows
	}
	return &Store{pool: pool, batchMaxRows: batchMaxRows}
}

// Close 释放连接池。
func (s *Store) Close() { s.pool.Close() }

// inTenantTx 在租户事务内执行 fn。与 repo.InTenantTx 同形态：
//   - 无租户上下文 → AR_TENANT_CONTEXT_MISSING（应用层保险；即使被绕过，
//     隔离视图谓词判 NULL 仍兜底 0 行 / 写入被 check_option 拒）；
//   - SET LOCAL ROLE airush_app：连接串用户在 dev/kind 可能是超级用户，
//     不降权则视图的租户谓词照样生效但基表零授权那道锁验不到；
//   - set_config(..., true)：事务级 GUC，连接归池自动失效（防串租户）。
func (s *Store) inTenantTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	tenantID, ok := tenancy.FromContext(ctx)
	if !ok {
		return apierror.New(apierror.CodeTenantContextMissing)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("tsstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE airush_app"); err != nil {
		return fmt.Errorf("tsstore: set role: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("tsstore: set tenant guc: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("tsstore: commit: %w", err)
	}
	return nil
}
