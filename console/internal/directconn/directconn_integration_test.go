//go:build integration

// spec-1.17 D5：directconn 对真实 PG 容器的连接生命周期与 test-connection 电池。
// 覆盖 T2（成功+版本）/T3（凭据错误）/T4（超时）/T6（无明文泄漏）/T7（池生命周期）。
package directconn_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sqlrush/airush/console/internal/credcrypto"
	"github.com/sqlrush/airush/console/internal/dbmigrate"
	"github.com/sqlrush/airush/console/internal/directconn"
	"github.com/sqlrush/airush/console/internal/repo"
	"github.com/sqlrush/airush/console/internal/tenancy"
	"github.com/sqlrush/airush/libs/apierror"
	"github.com/sqlrush/airush/testkit"
)

const devTenantID = "00000000-0000-0000-0000-000000000001"

type env struct {
	pgHost   string
	pgPort   int
	store    *repo.Store
	sealer   *credcrypto.Sealer
	mgr      *directconn.Manager
	tenant   context.Context
	password string
}

func newEnv(t *testing.T, cfg directconn.Config) *env {
	t.Helper()
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
	sealer, err := credcrypto.New(base64.StdEncoding.EncodeToString(kek), "v1")
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	host, port, password := parseConn(t, pg.ConnString)
	e := &env{
		pgHost: host, pgPort: port, store: store, sealer: sealer,
		mgr:      directconn.New(store, sealer, cfg),
		tenant:   tenancy.WithTenant(ctx, devTenantID),
		password: password,
	}
	t.Cleanup(e.mgr.Close)
	return e
}

// parseConn 从 testkit 连接串提取 host/port/password（容器映射端口）。
func parseConn(t *testing.T, connString string) (string, int, string) {
	t.Helper()
	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse conn string: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	pw, _ := u.User.Password()
	return u.Hostname(), port, pw
}

// createDirectDatasource 建一个直连数据源，凭据用真实 PG 的 user/password。
func (e *env) createDirectDatasource(t *testing.T, host string, port int, password string) string {
	t.Helper()
	var dsID string
	err := e.store.InTenantTx(e.tenant, func(ctx context.Context, tx repo.Tx) error {
		ciphertext, err := e.sealer.Seal([]byte(password))
		if err != nil {
			return err
		}
		credID, err := repo.InsertCredential(ctx, tx, "postgres", ciphertext, "v1")
		if err != nil {
			return err
		}
		ds, err := repo.InsertDatasource(ctx, tx, repo.DatasourceInput{
			Name: "dc-" + host, EngineFamily: "postgres", ConnectMode: "direct",
			CredentialID: &credID, Host: host, Port: port, DatabaseName: "postgres",
		})
		dsID = ds.ID
		return err
	})
	if err != nil {
		t.Fatalf("create direct datasource: %v", err)
	}
	return dsID
}

func TestTestConnectionSuccess(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	res, err := e.mgr.TestConnection(e.tenant, id)
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if !res.OK || !strings.Contains(strings.ToLower(res.Version), "postgre") {
		t.Fatalf("result = %+v", res)
	}
}

func TestTestConnectionBadCredential(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, "wrong-password")

	_, err := e.mgr.TestConnection(e.tenant, id)
	assertCode(t, err, apierror.CodeDatasourceConnectFailed)
	assertNoSecret(t, err.Error(), "wrong-password")
}

func TestTestConnectionUnreachable(t *testing.T) {
	cfg := directconn.DefaultConfig()
	cfg.ConnectTimeout = 1 * time.Second
	e := newEnv(t, cfg)
	// 不可路由地址 → 建连超时/失败
	id := e.createDirectDatasource(t, "10.255.255.1", 5432, e.password)

	_, err := e.mgr.TestConnection(e.tenant, id)
	if err == nil {
		t.Fatal("unreachable host accepted")
	}
	var ae *apierror.Error
	if !asError(err, &ae) ||
		(ae.Code != apierror.CodeDatasourceConnectFailed && ae.Code != apierror.CodeDatasourceTestTimeout) {
		t.Fatalf("err = %v, want CONNECT_FAILED/TEST_TIMEOUT", err)
	}
}

func TestPoolLifecycle(t *testing.T) {
	cfg := directconn.DefaultConfig()
	cfg.IdleTTL = 0 // 立即视为空闲
	e := newEnv(t, cfg)
	id := e.createDirectDatasource(t, e.pgHost, e.pgPort, e.password)

	if _, err := e.mgr.TestConnection(e.tenant, id); err != nil {
		t.Fatalf("prime pool: %v", err)
	}
	// ReapIdle 应回收（IdleTTL=0）；Destroy 幂等
	e.mgr.ReapIdle()
	e.mgr.Destroy(id)
	// 回收后重测仍成功（重建池）
	if _, err := e.mgr.TestConnection(e.tenant, id); err != nil {
		t.Fatalf("rebuild pool: %v", err)
	}
}

func TestModeMismatchRejected(t *testing.T) {
	e := newEnv(t, directconn.DefaultConfig())
	// connector 模式数据源 → test-connection 不适用
	var dsID string
	err := e.store.InTenantTx(e.tenant, func(ctx context.Context, tx repo.Tx) error {
		c, err := repo.InsertConnector(ctx, tx, repo.ConnectorInput{Name: "c1"})
		if err != nil {
			return err
		}
		ds, err := repo.InsertDatasource(ctx, tx, repo.DatasourceInput{
			Name: "conn-ds", EngineFamily: "postgres", ConnectMode: "connector",
			ConnectorID: &c.ID, Host: "h", Port: 5432,
		})
		dsID = ds.ID
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err = e.mgr.TestConnection(e.tenant, dsID)
	assertCode(t, err, apierror.CodeDatasourceModeMismatch)
}

// --- helpers ---

func assertCode(t *testing.T, err error, want apierror.Code) {
	t.Helper()
	var ae *apierror.Error
	if !asError(err, &ae) || ae.Code != want {
		t.Fatalf("err = %v, want code %s", err, want)
	}
}

func assertNoSecret(t *testing.T, s, secret string) {
	t.Helper()
	if strings.Contains(s, secret) {
		t.Fatalf("secret %q leaked in %q", secret, s)
	}
}

func asError(err error, target **apierror.Error) bool {
	ae, ok := apierror.FromError(err)
	if ok {
		*target = ae
	}
	return ok
}
