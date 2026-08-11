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

	sink := metrics.NewBufferSink(64)
	cfg := collector.Config{Interval: 300 * time.Millisecond, MinInterval: 100 * time.Millisecond, ReconcileEvery: 200 * time.Millisecond, Backoff: 100 * time.Millisecond}
	c := collector.New(store, mgr, sink, cfg, devTenantID, slog.New(slog.NewTextHandler(io.Discard, nil)))

	runCtx, cancel := context.WithCancel(ctx)
	go c.Run(runCtx)

	// T5：周期采集产多批
	waitFor(t, 4*time.Second, func() bool { return sink.Total() >= 2 })

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
			Name: "collector-ds", EngineFamily: "postgres", ConnectMode: "direct",
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
