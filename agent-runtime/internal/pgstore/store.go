// Package pgstore 是 codexgo threadstore.ThreadStore / agentgraph.AgentGraphStore 的控制面
// PostgreSQL 实现（spec-1.8 D2）：线程元数据、rollout 事件流（event sourcing）、外置输入队列、
// 子 agent 拓扑四张租户表（迁移 0006），全部 SQL 走租户事务（SET LOCAL ROLE airush_app +
// app.tenant_id，AD-10 由数据库强制），ctx 无租户 → fail-closed（AD-1：进程内零租户状态）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sqlrush/codexgo/pkg/threadstore"

	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/libs/tenancy"
)

// Options 配置 Store。
type Options struct {
	// DefaultModel 是新线程未指定逻辑模型名时写入 agent_threads.model 的缺省
	// （spec-1.7 逻辑名，缺省 chat-default）；ThreadStore.CreateThread 不携带模型，
	// runtime 随后经 SetThreadAttributes 改写。
	DefaultModel string
	// InlinePayloadLimit 是事件 payload 内联上限（字节），超出截断并写 payload_ref
	// （spec-1.8 §2.3：32KB）。<=0 用缺省。
	InlinePayloadLimit int
	// Now 可注入时钟（测试用）；nil 用 time.Now。
	Now func() time.Time
}

// DefaultInlinePayloadLimit 是事件 payload 内联上限缺省（32KB）。
const DefaultInlinePayloadLimit = 32 * 1024

// Store 持有连接池；并发安全。
type Store struct {
	pool  *pgxpool.Pool
	opts  Options
	clock func() time.Time
}

// New 用已建好的连接池构造 Store。
func New(pool *pgxpool.Pool, opts Options) *Store {
	if opts.DefaultModel == "" {
		opts.DefaultModel = "chat-default"
	}
	if opts.InlinePayloadLimit <= 0 {
		opts.InlinePayloadLimit = DefaultInlinePayloadLimit
	}
	clock := opts.Now
	if clock == nil {
		clock = time.Now
	}
	return &Store{pool: pool, opts: opts, clock: clock}
}

// Open 用连接串建池并构造 Store。
func Open(ctx context.Context, dbURL string, opts Options) (*Store, error) {
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("pgstore: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	return New(pool, opts), nil
}

// Close 关闭连接池。
func (s *Store) Close() { s.pool.Close() }

// Pool 暴露底层池（迁移/健康探针）。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// InTenantTx 在租户事务内执行 fn（与 console/internal/repo 同法）：
//   - ctx 未携带租户 → AR_TENANT_CONTEXT_MISSING（fail-closed）；
//   - SET LOCAL ROLE airush_app 降权后 FORCE RLS 对本事务生效；
//   - set_config(..., true) 事务级 GUC，连接归池自动失效（防串租户）。
func (s *Store) InTenantTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tenantID, ok := tenancy.FromContext(ctx)
	if !ok {
		return apierror.New(apierror.CodeTenantContextMissing)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin tenant tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL ROLE airush_app"); err != nil {
		return fmt.Errorf("pgstore: set role airush_app: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("pgstore: set tenant guc: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgstore: commit tenant tx: %w", err)
	}
	return nil
}

// tenantIDFrom 取 ctx 里的租户 id（调用方已过 InTenantTx，此处只做取值）。
func tenantIDFrom(ctx context.Context) string {
	id, _ := tenancy.FromContext(ctx)
	return id
}

// storeErr 把 pgx 错误统一包成 threadstore.ErrorKindInternal（保留原因）。
func storeErr(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	var storeError *threadstore.Error
	if errors.As(err, &storeError) {
		return err
	}
	if ae, ok := apierror.FromError(err); ok && ae.Code == apierror.CodeTenantContextMissing {
		return err
	}
	// 把原因拼进 message：threadstore.Error.Error() 只渲染 Message，%w 链上看不到 Cause。
	return threadstore.NewInternalError(err, format+": %v", append(append([]any{}, args...), err)...)
}
