//go:build integration

package pgstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	gomigrate "github.com/golang-migrate/migrate/v4"
	// pgx5 数据库驱动注册 "pgx5" URL scheme。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sqlrush/airush/console/migrations"
	"github.com/sqlrush/airush/libs/tenancy"
	"github.com/sqlrush/airush/testkit"
)

// 包级共享：一个 PG 容器 + 全量控制面迁移（0001..0006）+ 一个连接池；
// 用例隔离靠"每用例一个租户"（RLS 天然隔离，比每用例一个 schema 便宜且更贴近生产形态）。
var (
	testPool  *pgxpool.Pool
	testStore *Store

	tenantMu  sync.Mutex
	tenantByT = map[string]string{}
)

func TestMain(m *testing.M) {
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx := context.Background()
	pg, err := testkit.StartPostgres(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres: %v\n", err)
		return 1
	}
	defer func() { _ = pg.Terminate(context.Background()) }()

	if err := migrateUp(pg.ConnString); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}
	pool, err := pgxpool.New(ctx, pg.ConnString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool: %v\n", err)
		return 1
	}
	defer pool.Close()
	testPool = pool
	testStore = New(pool, Options{InlinePayloadLimit: testInlineLimit})
	return m.Run()
}

// testInlineLimit 把内联上限压小，让截断用例不用造 32KB payload。
const testInlineLimit = 2048

// migrateUp 用 console 内嵌迁移把库升到最新（与 console/internal/dbmigrate 同一驱动与源；
// 那是 console 的 internal 包，agent-runtime 不能导入，此处只复刻 up 这一条路径）。
func migrateUp(dbURL string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dbURL, prefix) {
			dbURL = "pgx5://" + strings.TrimPrefix(dbURL, prefix)
			break
		}
	}
	m, err := gomigrate.NewWithSourceInstance("iofs", src, dbURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, gomigrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	srcErr, dbErr := m.Close()
	return errors.Join(srcErr, dbErr)
}

// tenantFor 给一个测试（按 t.Name()）分配并登记一个专属租户；同一 t 反复调用得到同一租户
// （contracttest 的 NewStore 与 Context 回调各自调用一次）。
func tenantFor(t *testing.T) string {
	t.Helper()
	tenantMu.Lock()
	defer tenantMu.Unlock()
	if id, ok := tenantByT[t.Name()]; ok {
		return id
	}
	id := newTenant(t)
	tenantByT[t.Name()] = id
	return id
}

// newTenant 插入一个新租户主档并返回 id。
func newTenant(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("random suffix: %v", err)
	}
	id := uuid.NewString()
	slug := "t-" + hex.EncodeToString(buf)
	if _, err := testPool.Exec(context.Background(), `INSERT INTO tenants (id, name, slug) VALUES ($1, $2, $3)`, id, "租户 "+slug, slug); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return id
}

// tenantCtx 返回携带该测试专属租户的 ctx。
func tenantCtx(t *testing.T) context.Context {
	t.Helper()
	return tenancy.WithTenant(context.Background(), tenantFor(t))
}

// otherTenantCtx 返回另一个（新建）租户的 ctx，用于跨租户不可见断言。
func otherTenantCtx(t *testing.T) context.Context {
	t.Helper()
	return tenancy.WithTenant(context.Background(), newTenant(t))
}

// waitFor 轮询直到 cond 为真（用于依赖 now() 的心跳类断言）。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
