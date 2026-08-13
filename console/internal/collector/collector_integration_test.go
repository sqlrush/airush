//go:build integration

// spec-1.3 D3/D5：采集调度器对真实 PG 的周期采集 + 生命周期（T5）。
package collector_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/collector"
	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/metrics"
	"github.com/sqlrush/airush/testkit"
)

const devTenantID = "00000000-0000-0000-0000-000000000001"

// countingConnector 记录 Connector 通道触发次数（TriggerCollect 由调度器周期调用）。
type countingConnector struct {
	calls atomic.Int64
	mu    sync.Mutex
	kinds map[string]int
}

func (c *countingConnector) TriggerCollect(_ context.Context, _, _, _, kind string) error {
	c.calls.Add(1)
	c.mu.Lock()
	c.kinds[kind]++
	c.mu.Unlock()
	return nil
}

// kindCount 返回该 kind 被触发的次数。
func (c *countingConnector) kindCount(kind string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kinds[kind]
}

func TestCollectorPeriodicAndLifecycle(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := repo.New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(store.Close)

	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	sealer, _ := credcrypto.New(base64.StdEncoding.EncodeToString(kek), "v1")
	mgr := directconn.New(store, sealer, directconn.DefaultConfig())
	t.Cleanup(mgr.Close)

	host, port, password := parseConn(t, pg.ConnString)
	tctx := tenancy.WithTenant(ctx, devTenantID)
	dsID := createDirectDS(t, store, sealer, tctx, host, port, password)
	createConnectorDS(t, store, tctx) // Connector 通道数据源：调度器经 fake 触发
	// T6：不可达 Direct 数据源 → 采集失败退避，不阻断健康实例（隔离性）。
	createNamedDirectDS(t, store, sealer, tctx, "collector-ds-bad", "127.0.0.1", 1, "x")

	sink := metrics.NewBufferSink(64)
	conn := &countingConnector{kinds: map[string]int{}}
	cfg := collector.Config{
		Interval: 300 * time.Millisecond, MinInterval: 100 * time.Millisecond,
		SlowlogInterval: 300 * time.Millisecond, SlowlogMinInterval: 100 * time.Millisecond,
		MetaInterval: 300 * time.Millisecond, MetaMinInterval: 100 * time.Millisecond,
		ReconcileEvery: 200 * time.Millisecond, Backoff: 100 * time.Millisecond,
	}
	c := collector.New(store, mgr, conn, sink, sink, cfg, devTenantID, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runCtx, cancel := context.WithCancel(ctx)
	go c.Run(runCtx)

	// T5：周期采集产多批（Direct → sink）
	waitFor(t, 4*time.Second, func() bool { return sink.Total() >= 2 })
	// Connector 通道由调度器经 gateway 触发（fake 记录次数）
	waitFor(t, 4*time.Second, func() bool { return conn.calls.Load() >= 1 })

	// 生命周期：删除数据源 → 采集停（计数不再显著增长）
	if err := store.InTenantTx(tctx, func(ctx context.Context, tx repo.Tx) error {
		return repo.DeleteDatasource(ctx, tx, dsID)
	}); err != nil {
		t.Fatalf("delete ds: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // 让 reconcile 停循环
	before := sink.Total()
	time.Sleep(800 * time.Millisecond)
	if grew := sink.Total() - before; grew > 1 {
		t.Fatalf("collection continued after datasource removal: +%d batches", grew)
	}
	cancel()

	if b, ok := sink.Latest(); !ok || b.DatasourceID != dsID || len(b.Metrics) == 0 {
		t.Fatalf("latest batch = %+v ok=%v", b, ok)
	}
}

func createDirectDS(t *testing.T, store *repo.Store, sealer *credcrypto.Sealer, tctx context.Context, host string, port int, password string) string {
	t.Helper()
	return createNamedDirectDS(t, store, sealer, tctx, "collector-ds", host, port, password)
}

func createNamedDirectDS(t *testing.T, store *repo.Store, sealer *credcrypto.Sealer, tctx context.Context, name, host string, port int, password string) string {
	t.Helper()
	var id string
	err := store.InTenantTx(tctx, func(ctx context.Context, tx repo.Tx) error {
		ct, err := sealer.Seal([]byte(password))
		if err != nil {
			return err
		}
		credID, err := repo.InsertCredential(ctx, tx, "postgres", ct, "v1")
		if err != nil {
			return err
		}
		ds, err := repo.InsertDatasource(ctx, tx, repo.DatasourceInput{
			Name: name, EngineFamily: "postgres", ConnectMode: "direct",
			CredentialID: &credID, Host: host, Port: port, DatabaseName: "postgres",
		})
		id = ds.ID
		return err
	})
	if err != nil {
		t.Fatalf("create ds: %v", err)
	}
	return id
}

// createConnectorDS 建一个 connector 模式数据源（含前置 connector 行满足 FK）。
func createConnectorDS(t *testing.T, store *repo.Store, tctx context.Context) string {
	t.Helper()
	var id string
	err := store.InTenantTx(tctx, func(ctx context.Context, tx repo.Tx) error {
		conn, err := repo.InsertConnector(ctx, tx, repo.ConnectorInput{Name: "collector-conn", Location: "dev"})
		if err != nil {
			return err
		}
		ds, err := repo.InsertDatasource(ctx, tx, repo.DatasourceInput{
			Name: "collector-conn-ds", EngineFamily: "postgres", ConnectMode: "connector",
			ConnectorID: &conn.ID, Host: "unused", Port: 5432, DatabaseName: "postgres",
		})
		id = ds.ID
		return err
	})
	if err != nil {
		t.Fatalf("create connector ds: %v", err)
	}
	return id
}

func parseConn(t *testing.T, connString string) (string, int, string) {
	t.Helper()
	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse conn: %v", err)
	}
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	return u.Hostname(), port, pw
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestCollectorDrivesEveryKind spec-1.4 T11：调度器为每个数据源起 datasource×kind
// 条循环——Direct 侧四类快照/指标都要落到对应 Sink，Connector 侧四类都要下发。
func TestCollectorDrivesEveryKind(t *testing.T) {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	if err := dbmigrate.RunWithURL(pg.ConnString, []string{"up"}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := repo.New(ctx, pg.ConnString)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(store.Close)

	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	sealer, _ := credcrypto.New(base64.StdEncoding.EncodeToString(kek), "v1")
	mgr := directconn.New(store, sealer, directconn.DefaultConfig())
	t.Cleanup(mgr.Close)
	tctx := tenancy.WithTenant(ctx, devTenantID)

	host, port, password := parseConn(t, pg.ConnString)
	createNamedDirectDS(t, store, sealer, tctx, "kinds-direct", host, port, password)
	createConnectorDS(t, store, tctx)

	sink := metrics.NewBufferSink(64)
	conn := &countingConnector{kinds: map[string]int{}}
	cfg := collector.Config{
		Interval: 200 * time.Millisecond, MinInterval: 100 * time.Millisecond,
		SlowlogInterval: 200 * time.Millisecond, SlowlogMinInterval: 100 * time.Millisecond,
		MetaInterval: 200 * time.Millisecond, MetaMinInterval: 100 * time.Millisecond,
		ReconcileEvery: 200 * time.Millisecond, Backoff: 100 * time.Millisecond,
	}
	c := collector.New(store, mgr, conn, sink, sink, cfg, devTenantID,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.Run(runCtx)

	// Direct 侧：指标批 + 三类快照都要收到。
	waitFor(t, 8*time.Second, func() bool { return sink.Total() >= 1 })
	for _, kind := range []string{
		metrics.SnapshotKindSlowlog, metrics.SnapshotKindSchema, metrics.SnapshotKindConfig,
	} {
		waitFor(t, 8*time.Second, func() bool {
			_, ok := sink.LatestSnapshotOf(kind)
			return ok
		})
	}
	// 慢查询在无 pg_stat_statements 的测试 PG 上应是能力降级快照，而非缺席。
	slowlog, _ := sink.LatestSnapshotOf(metrics.SnapshotKindSlowlog)
	if !slowlog.CapabilityMissing && len(slowlog.SlowQueries) == 0 {
		t.Fatalf("slowlog snapshot neither degraded nor populated: %+v", slowlog)
	}

	// Connector 侧：四类都要下发（kind 必须传到 gateway）。
	for _, kind := range []string{
		"metrics", metrics.SnapshotKindSlowlog, metrics.SnapshotKindSchema, metrics.SnapshotKindConfig,
	} {
		waitFor(t, 8*time.Second, func() bool { return conn.kindCount(kind) >= 1 })
	}
}
