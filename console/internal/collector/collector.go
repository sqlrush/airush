// Package collector 是采集调度器（spec-1.3 D3）：每 datasource 一周期采集循环，
// 经 Accessor/Querier 触发探针 → Sink。采集失败退避不阻断其他实例；datasource
// 增删即起停对应循环。Stage 1 驱动 Direct 通道；Connector 通道随 D4 接入同一调度。
package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/metrics"
)

// Config 采集调度参数。
type Config struct {
	Interval       time.Duration // 采集间隔（下限护栏 MinInterval）
	MinInterval    time.Duration
	ReconcileEvery time.Duration // 多久对齐一次 datasource 集合
	Backoff        time.Duration // 采集失败初始退避（指数，上限 5min）
}

// DefaultConfig spec-1.3 §2.3 默认。
func DefaultConfig() Config {
	return Config{
		Interval:       60 * time.Second,
		MinInterval:    15 * time.Second,
		ReconcileEvery: 30 * time.Second,
		Backoff:        5 * time.Second,
	}
}

func (c Config) effectiveInterval() time.Duration {
	if c.Interval < c.MinInterval {
		return c.MinInterval
	}
	return c.Interval
}

// Collector 驱动周期采集。
type Collector struct {
	store    *repo.Store
	direct   *directconn.Manager
	sink     metrics.Sink
	cfg      Config
	tenantID string
	logger   *slog.Logger

	mu    sync.Mutex
	loops map[string]context.CancelFunc // datasourceID → stop
}

// New 构造 Collector（tenantID 为 Stage 1 默认租户，spec-2.2 认证接管后按租户展开）。
func New(store *repo.Store, direct *directconn.Manager, sink metrics.Sink, cfg Config, tenantID string, logger *slog.Logger) *Collector {
	return &Collector{
		store: store, direct: direct, sink: sink, cfg: cfg,
		tenantID: tenantID, logger: logger, loops: map[string]context.CancelFunc{},
	}
}

// Run 周期对齐 datasource 集合并管理各自采集循环，直到 ctx 取消。
func (c *Collector) Run(ctx context.Context) {
	c.reconcile(ctx)
	ticker := time.NewTicker(c.cfg.ReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.stopAll()
			return
		case <-ticker.C:
			c.reconcile(ctx)
		}
	}
}

// reconcile 对齐：为新 direct datasource 起采集循环，为消失的停循环。
func (c *Collector) reconcile(ctx context.Context) {
	want := c.listDirectDatasources(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	// 起新循环
	for id, engine := range want {
		if _, ok := c.loops[id]; ok {
			continue
		}
		loopCtx, cancel := context.WithCancel(ctx)
		c.loops[id] = cancel
		go c.loop(loopCtx, id, engine)
	}
	// 停消失的
	for id, cancel := range c.loops {
		if _, ok := want[id]; !ok {
			cancel()
			delete(c.loops, id)
		}
	}
}

// loop 单 datasource 的采集循环：间隔 + 抖动，采集失败指数退避（上限 5min），
// 循环顺序执行天然单采集在途（防堆积）。
func (c *Collector) loop(ctx context.Context, datasourceID, engineFamily string) {
	interval := c.cfg.effectiveInterval()
	backoff := c.cfg.Backoff
	// 启动抖动：首个周期在 [0, interval) 内偏移——用 datasourceID 派生确定性偏移，
	// 避免引入随机源且测试可复现。掩码为非负 int64 后取模，杜绝溢出。
	jitter := time.Duration(int64(hashOffset(datasourceID)&0x7fffffffffffffff) % int64(interval))
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.collectOnce(ctx, datasourceID, engineFamily); err != nil {
			c.logger.Warn("metrics collect failed", "datasource_id", datasourceID, "err", err)
			timer.Reset(backoff)
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = c.cfg.Backoff
		timer.Reset(interval)
	}
}

// collectOnce 采一次 → Sink。
func (c *Collector) collectOnce(ctx context.Context, datasourceID, engineFamily string) error {
	q := c.direct.MetricsQuerier(datasourceID)
	tctx := tenancy.WithTenant(ctx, c.tenantID)
	probe := metrics.Probe{DatasourceID: datasourceID, EngineFamily: engineFamily}
	batch, err := probe.Collect(tctx, q)
	if err != nil {
		return err
	}
	return c.sink.Publish(ctx, batch)
}

// listDirectDatasources 列本租户 direct 模式数据源（id → engine_family）。
func (c *Collector) listDirectDatasources(ctx context.Context) map[string]string {
	out := map[string]string{}
	tctx := tenancy.WithTenant(ctx, c.tenantID)
	err := c.store.InTenantTx(tctx, func(ctx context.Context, tx repo.Tx) error {
		var cursor *repo.PageCursor
		for {
			items, err := repo.ListDatasources(ctx, tx, cursor, 200)
			if err != nil {
				return err
			}
			for _, ds := range items {
				if ds.ConnectMode == "direct" {
					out[ds.ID] = ds.EngineFamily
				}
			}
			if len(items) < 200 {
				break
			}
			last := items[len(items)-1]
			cursor = &repo.PageCursor{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		return nil
	})
	if err != nil {
		c.logger.Warn("list direct datasources failed", "err", err)
	}
	return out
}

func (c *Collector) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, cancel := range c.loops {
		cancel()
		delete(c.loops, id)
	}
}

// hashOffset 由 datasourceID 派生确定性偏移（FNV-1a）。
func hashOffset(s string) uint64 {
	const offset64 = 1469598103934665603
	const prime64 = 1099511628211
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}
