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

// KindMetrics 是指标采集类型（spec-1.3）；快照类用 metrics.SnapshotKind*。
const KindMetrics = "metrics"

// collectKinds 是每个数据源要驱动的采集类型，顺序稳定。
var collectKinds = []string{
	KindMetrics,
	metrics.SnapshotKindSlowlog,
	metrics.SnapshotKindSchema,
	metrics.SnapshotKindConfig,
}

// target 是一个采集目标：一个数据源的一个采集类型（每个 target 一条独立循环，
// 各自间隔与退避互不影响）。
type target struct {
	datasourceID string
	engineFamily string
	mode         string // "direct" | "connector"
	connectorID  string // connector 模式必填
	kind         string
}

// key 是循环表的键：同一数据源的不同 kind 是不同循环。
func (t target) key() string { return t.datasourceID + "|" + t.kind }

// ErrConnectorPathDisabled 表示遇到 connector 数据源但未配置 gateway 客户端。
var ErrConnectorPathDisabled = errors.New("connector collect path not configured")

// Config 采集调度参数。分 kind 间隔（spec-1.4 §2.4 §8 Q4）：慢查询统计是累积视图，
// 5min 粒度够用；表结构/配置变更低频，1h 快照成本低；各自带下限护栏防误配高频压库。
type Config struct {
	Interval    time.Duration // 指标采集间隔（下限护栏 MinInterval）
	MinInterval time.Duration

	SlowlogInterval    time.Duration
	SlowlogMinInterval time.Duration

	MetaInterval    time.Duration // schema / config 共用
	MetaMinInterval time.Duration

	ReconcileEvery time.Duration // 多久对齐一次 datasource 集合
	Backoff        time.Duration // 采集失败初始退避（指数，上限 5min）
}

// DefaultConfig spec-1.3 §2.3 + spec-1.4 §2.4 默认。
func DefaultConfig() Config {
	return Config{
		Interval:           60 * time.Second,
		MinInterval:        15 * time.Second,
		SlowlogInterval:    300 * time.Second,
		SlowlogMinInterval: 60 * time.Second,
		MetaInterval:       3600 * time.Second,
		MetaMinInterval:    300 * time.Second,
		ReconcileEvery:     30 * time.Second,
		Backoff:            5 * time.Second,
	}
}

func (c Config) effectiveInterval() time.Duration {
	return atLeast(c.Interval, c.MinInterval)
}

// intervalFor 返回该采集类型的生效间隔（已过下限护栏）。
func (c Config) intervalFor(kind string) time.Duration {
	switch kind {
	case metrics.SnapshotKindSlowlog:
		return atLeast(c.SlowlogInterval, c.SlowlogMinInterval)
	case metrics.SnapshotKindSchema, metrics.SnapshotKindConfig:
		return atLeast(c.MetaInterval, c.MetaMinInterval)
	default:
		return c.effectiveInterval()
	}
}

func atLeast(value, floor time.Duration) time.Duration {
	if value < floor {
		return floor
	}
	return value
}

// Collector 驱动周期采集。
type Collector struct {
	store        *repo.Store
	direct       *directconn.Manager
	connector    ConnectorCollector // nil 时 connector 数据源被跳过并告警
	sink         metrics.Sink
	snapshotSink metrics.SnapshotSink
	cfg          Config
	tenantID     string
	logger       *slog.Logger

	mu    sync.Mutex
	loops map[string]context.CancelFunc // target.key() → stop
}

// New 构造 Collector（tenantID 为 Stage 1 默认租户，spec-2.2 认证接管后按租户展开）。
// connector 为 nil 时仅驱动 Direct 通道（connector 数据源被跳过）。
func New(
	store *repo.Store, direct *directconn.Manager, connector ConnectorCollector,
	sink metrics.Sink, snapshotSink metrics.SnapshotSink,
	cfg Config, tenantID string, logger *slog.Logger,
) *Collector {
	return &Collector{
		store: store, direct: direct, connector: connector,
		sink: sink, snapshotSink: snapshotSink, cfg: cfg,
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

// reconcile 对齐：为新目标（数据源×采集类型）起循环，为消失的停循环。
func (c *Collector) reconcile(ctx context.Context) {
	want := c.listTargets(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	// 起新循环
	for key, t := range want {
		if _, ok := c.loops[key]; ok {
			continue
		}
		loopCtx, cancel := context.WithCancel(ctx)
		c.loops[key] = cancel
		go c.loop(loopCtx, t)
	}
	// 停消失的（datasource 删除即停其全部 kind 循环）
	for key, cancel := range c.loops {
		if _, ok := want[key]; !ok {
			cancel()
			delete(c.loops, key)
		}
	}
}

// loop 单 datasource 的采集循环：间隔 + 抖动，采集失败指数退避（上限 5min），
// 循环顺序执行天然单采集在途（防堆积）。
func (c *Collector) loop(ctx context.Context, t target) {
	interval := c.cfg.intervalFor(t.kind)
	backoff := c.cfg.Backoff
	// 启动抖动：首个周期在 [0, interval) 内偏移——用 target key 派生确定性偏移，
	// 避免引入随机源且测试可复现（同数据源的不同 kind 也因此错开）。0x7fff… 掩码
	// 保非负、% interval 保 < interval，转 int64 恒安全（gosec 无法证明该界）。
	//nolint:gosec // 掩码+取模保证结果在 [0, interval)，无溢出
	jitter := time.Duration(int64(hashOffset(t.key())&0x7fffffffffffffff) % int64(interval))
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := c.collectOnce(ctx, t); err != nil {
			c.logger.Warn("collect failed",
				"datasource_id", t.datasourceID, "kind", t.kind, "mode", t.mode, "err", err)
			timer.Reset(backoff)
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		// 采集心跳（可观测性 + dev-verify 断言锚点）；Connector 通道数据落 gateway Sink。
		c.logger.Info("metrics collected",
			"datasource_id", t.datasourceID, "kind", t.kind, "mode", t.mode)
		backoff = c.cfg.Backoff
		timer.Reset(interval)
	}
}

// collectOnce 按通道采一次。Direct 本地跑探针 → 本进程 Sink；Connector 触发 gateway
// 下发（数据由 gateway 落其 Sink，spec-1.3 §2.4 双 Sink），本调用只判成败驱动退避。
func (c *Collector) collectOnce(ctx context.Context, t target) error {
	if t.mode == "connector" {
		if c.connector == nil {
			return ErrConnectorPathDisabled
		}
		return c.connector.TriggerCollect(ctx, t.connectorID, t.datasourceID, t.engineFamily, t.kind)
	}
	if t.kind == KindMetrics {
		return c.collectMetricsDirect(ctx, t)
	}
	return c.collectSnapshotDirect(ctx, t)
}

func (c *Collector) collectMetricsDirect(ctx context.Context, t target) error {
	tctx := tenancy.WithTenant(ctx, c.tenantID)
	probe := metrics.Probe{DatasourceID: t.datasourceID, EngineFamily: t.engineFamily}
	batch, err := probe.Collect(tctx, c.direct.MetricsQuerier(t.datasourceID))
	if err != nil {
		return err
	}
	return c.sink.Publish(ctx, batch)
}

// collectSnapshotDirect 采一份快照。能力缺失（如未装 pg_stat_statements）是成功
// 路径：快照照常上报并带 CapabilityMissing 标记，调度按正常间隔继续、不进退避。
func (c *Collector) collectSnapshotDirect(ctx context.Context, t target) error {
	tctx := tenancy.WithTenant(ctx, c.tenantID)
	probe := metrics.SnapshotProbe{DatasourceID: t.datasourceID, EngineFamily: t.engineFamily}
	snapshot, err := probe.Collect(tctx, c.direct.SnapshotQuerier(t.datasourceID), t.kind)
	if err != nil {
		return err
	}
	return c.snapshotSink.PublishSnapshot(ctx, snapshot)
}

// targetsFor 把一条数据源展开为它的全部采集目标（每个 kind 一个）：direct 直采；
// 带非空 connector_id 的 connector 模式经 gateway 触发；其余（未接入/缺
// connector_id）不采。
func targetsFor(ds repo.Datasource) []target {
	base, ok := channelFor(ds)
	if !ok {
		return nil
	}
	out := make([]target, 0, len(collectKinds))
	for _, kind := range collectKinds {
		t := base
		t.kind = kind
		out = append(out, t)
	}
	return out
}

// channelFor 解析数据源的通道归属（不含 kind）。
func channelFor(ds repo.Datasource) (target, bool) {
	switch {
	case ds.ConnectMode == "direct":
		return target{datasourceID: ds.ID, engineFamily: ds.EngineFamily, mode: "direct"}, true
	case ds.ConnectMode == "connector" && ds.ConnectorID != nil && *ds.ConnectorID != "":
		return target{
			datasourceID: ds.ID, engineFamily: ds.EngineFamily,
			mode: "connector", connectorID: *ds.ConnectorID,
		}, true
	default:
		return target{}, false
	}
}

// listTargets 列本租户全部采集目标（数据源 × 采集类型），键为 target.key()。
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
				for _, t := range targetsFor(ds) {
					out[t.key()] = t
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
