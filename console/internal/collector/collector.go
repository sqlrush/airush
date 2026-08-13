// Package collector 是采集调度器（spec-1.3 D3）：每 datasource 一周期采集循环，
// 经 Accessor/Querier 触发探针 → Sink。采集失败退避不阻断其他实例；datasource
// 增删即起停对应循环。Direct 通道在本进程跑探针；Connector 通道经 gateway 下发到
// 客户侧连接器采集回传（D4），两通道产出同一 metrics.Batch 落同一 Sink。
package collector

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/metrics"
)

// target 是一个采集目标的通道描述（Direct 用本地探针；Connector 经 gateway 下发）。
type target struct {
	engineFamily string
	mode         string // "direct" | "connector"
	connectorID  string // connector 模式必填
}

// ErrConnectorPathDisabled 表示遇到 connector 数据源但未配置 gateway 客户端。
var ErrConnectorPathDisabled = errors.New("connector collect path not configured")

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
	store     *repo.Store
	direct    *directconn.Manager
	connector ConnectorCollector // nil 时 connector 数据源被跳过并告警
	sink      metrics.Sink
	cfg       Config
	tenantID  string
	logger    *slog.Logger

	mu    sync.Mutex
	loops map[string]context.CancelFunc // datasourceID → stop
}

// New 构造 Collector（tenantID 为 Stage 1 默认租户，spec-2.2 认证接管后按租户展开）。
// connector 为 nil 时仅驱动 Direct 通道（connector 数据源被跳过）。
func New(store *repo.Store, direct *directconn.Manager, connector ConnectorCollector, sink metrics.Sink, cfg Config, tenantID string, logger *slog.Logger) *Collector {
	return &Collector{
		store: store, direct: direct, connector: connector, sink: sink, cfg: cfg,
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

// reconcile 对齐：为新数据源起采集循环，为消失的停循环。
func (c *Collector) reconcile(ctx context.Context) {
	want := c.listTargets(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	// 起新循环
	for id, t := range want {
		if _, ok := c.loops[id]; ok {
			continue
		}
		loopCtx, cancel := context.WithCancel(ctx)
		c.loops[id] = cancel
		go c.loop(loopCtx, id, t)
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
func (c *Collector) loop(ctx context.Context, datasourceID string, t target) {
	interval := c.cfg.effectiveInterval()
	backoff := c.cfg.Backoff
	// 启动抖动：首个周期在 [0, interval) 内偏移——用 datasourceID 派生确定性偏移，
	// 避免引入随机源且测试可复现。0x7fff… 掩码保非负、% interval 保 < interval，
	// 转 int64 恒安全（gosec 无法证明该界，故显式 nolint）。
	//nolint:gosec // 掩码+取模保证结果在 [0, interval)，无溢出
	jitter := time.Duration(int64(hashOffset(datasourceID)&0x7fffffffffffffff) % int64(interval))
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.collectOnce(ctx, datasourceID, t); err != nil {
			c.logger.Warn("metrics collect failed",
				"datasource_id", datasourceID, "mode", t.mode, "err", err)
			timer.Reset(backoff)
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		// 采集心跳（可观测性 + dev-verify Direct 断言锚点）；Connector 通道数据落 gateway Sink。
		c.logger.Info("metrics collected", "datasource_id", datasourceID, "mode", t.mode)
		backoff = c.cfg.Backoff
		timer.Reset(interval)
	}
}

// collectOnce 按通道采一次。Direct 本地跑探针 → 本进程 Sink；Connector 触发 gateway
// 下发（数据由 gateway 落其 Sink，spec-1.3 §2.4 双 Sink），本调用只判成败驱动退避。
func (c *Collector) collectOnce(ctx context.Context, datasourceID string, t target) error {
	switch t.mode {
	case "connector":
		if c.connector == nil {
			return ErrConnectorPathDisabled
		}
		return c.connector.TriggerCollect(ctx, t.connectorID, datasourceID, t.engineFamily)
	default: // direct
		q := c.direct.MetricsQuerier(datasourceID)
		tctx := tenancy.WithTenant(ctx, c.tenantID)
		probe := metrics.Probe{DatasourceID: datasourceID, EngineFamily: t.engineFamily}
		batch, err := probe.Collect(tctx, q)
		if err != nil {
			return err
		}
		return c.sink.Publish(ctx, batch)
	}
}

// targetFor 把一条数据源映射为采集目标：direct 直采；带非空 connector_id 的 connector
// 模式经 gateway 触发；其余（未接入/缺 connector_id）不采（ok=false）。
func targetFor(ds repo.Datasource) (target, bool) {
	switch {
	case ds.ConnectMode == "direct":
		return target{engineFamily: ds.EngineFamily, mode: "direct"}, true
	case ds.ConnectMode == "connector" && ds.ConnectorID != nil && *ds.ConnectorID != "":
		return target{engineFamily: ds.EngineFamily, mode: "connector", connectorID: *ds.ConnectorID}, true
	default:
		return target{}, false
	}
}

// listTargets 列本租户可采集数据源（direct + 带 connector_id 的 connector 模式）。
func (c *Collector) listTargets(ctx context.Context) map[string]target {
	out := map[string]target{}
	tctx := tenancy.WithTenant(ctx, c.tenantID)
	err := c.store.InTenantTx(tctx, func(ctx context.Context, tx repo.Tx) error {
		var cursor *repo.PageCursor
		for {
			items, err := repo.ListDatasources(ctx, tx, cursor, 200)
			if err != nil {
				return err
			}
			for _, ds := range items {
				if t, ok := targetFor(ds); ok {
					out[ds.ID] = t
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
		c.logger.Warn("list datasources failed", "err", err)
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
