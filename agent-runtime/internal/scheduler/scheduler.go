// Package scheduler 是会话调度器（spec-1.8 D4）：每租户并发 turn 上限、跨 pod 的队列派发
// （sweeper）。会话内逐轮串行由 pgstore.ClaimTurn（一个线程同一时刻只被一个 pod 持有）与
// codexgo 的单活动 turn 保证；会话间并行由每个 pod 独立跑各自持有的线程实现。
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sqlrush/airush/agent-runtime/internal/pgstore"
	"github.com/sqlrush/airush/libs/tenancy"
)

// TenantLimiter 是每租户并发 turn 上限（Stage 1：全部租户同一上限 = 单 pod 配置值；
// 每租户配额来自控制面是 1.7 之后的事）。并发安全。
type TenantLimiter struct {
	max int
	mu  sync.Mutex
	cur map[string]int
}

// NewTenantLimiter 构造；max<=0 表示不限。
func NewTenantLimiter(max int) *TenantLimiter {
	return &TenantLimiter{max: max, cur: map[string]int{}}
}

// TryAcquire 尝试给租户占一个额度。
func (l *TenantLimiter) TryAcquire(tenantID string) bool {
	if l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur[tenantID] >= l.max {
		return false
	}
	l.cur[tenantID]++
	return true
}

// Release 归还一个额度（多还不会负）。
func (l *TenantLimiter) Release(tenantID string) {
	if l.max <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur[tenantID] > 0 {
		l.cur[tenantID]--
	}
	if l.cur[tenantID] == 0 {
		delete(l.cur, tenantID)
	}
}

// InFlight 返回租户当前占用数（测试/观测）。
func (l *TenantLimiter) InFlight(tenantID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cur[tenantID]
}

// Dispatcher 是 sweeper 需要的最小运行时接口（runtime.Engine 满足）。
type Dispatcher interface {
	// Dispatch 按 ctx 里的租户派发线程队列里的待接纳输入（未接纳不是错误）。
	Dispatch(ctx context.Context, threadID string) error
	Draining() bool
}

// PendingLister 列出全部有待接纳输入的线程（pgstore.Store 满足）。
type PendingLister interface {
	ThreadsWithPendingInputs(ctx context.Context) ([]pgstore.PendingThread, error)
}

// Sweeper 定期扫描外置队列，把"有输入但没人处理"的线程派发出去：等额度的排队 turn、
// 被别的 pod 拒之门外的 steer（持有方已释放）、恢复后重投的输入，都由它兜底。
type Sweeper struct {
	store    PendingLister
	dispatch Dispatcher
	interval time.Duration
	logger   *slog.Logger
}

// NewSweeper 构造；interval<=0 用 2s。
func NewSweeper(store PendingLister, d Dispatcher, interval time.Duration, logger *slog.Logger) *Sweeper {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{store: store, dispatch: d, interval: interval, logger: logger}
}

// Run 阻塞运行到 ctx 结束。
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep 扫一轮；排水中不派发。
func (s *Sweeper) Sweep(ctx context.Context) {
	if s.dispatch.Draining() {
		return
	}
	pending, err := s.store.ThreadsWithPendingInputs(ctx)
	if err != nil {
		s.logger.Warn("sweep: list pending threads failed", "error", err)
		return
	}
	for _, p := range pending {
		tctx := tenancy.WithTenant(ctx, p.TenantID)
		if err := s.dispatch.Dispatch(tctx, p.ThreadID); err != nil {
			s.logger.Warn("sweep: dispatch failed", "tenant_id", p.TenantID, "thread_id", p.ThreadID, "error", err)
		}
	}
}
