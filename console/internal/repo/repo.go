// Package repo 是控制面数据访问基座（spec-1.1 D3）：所有租户数据操作必须经
// InTenantTx——事务内 SET LOCAL ROLE airush_app + app.tenant_id，即 RLS 的
// 应用层执行路径。httpapi 直接 import pgx 被 depguard 硬禁，本包是唯一 DB 入口。
package repo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
)

// Tx 是租户事务句柄的对外别名：httpapi 经它调用本包查询函数而不 import pgx
// （depguard console-httpapi-no-direct-db 的配套出口）。
type Tx = pgx.Tx

// Store 持有连接池；构造经 New，查询函数按域分文件（datasources.go 等）。
type Store struct {
	pool *pgxpool.Pool
}

// New 建池并 ping；失败返回错误（启动期 fail fast）。
func New(ctx context.Context, dbURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping control-plane db: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close 释放连接池。
func (s *Store) Close() { s.pool.Close() }

// InTenantTx 在租户事务内执行 fn：
//   - ctx 未携带租户 → AR_TENANT_CONTEXT_MISSING（fail-closed 应用层保险；
//     即使此检查被绕过，RLS policy 判 NULL 仍兜底 0 行）；
//   - SET LOCAL ROLE airush_app：连接串用户可能是超级用户（dev/kind），
//     降权后 FORCE RLS 才对本事务生效；
//   - set_config(..., true)：事务级 GUC，连接归池自动失效（防串租户）。
func (s *Store) InTenantTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tenantID, ok := tenancy.FromContext(ctx)
	if !ok {
		return apierror.New(apierror.CodeTenantContextMissing)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE airush_app"); err != nil {
		return fmt.Errorf("set role airush_app: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant tx: %w", err)
	}
	return nil
}
