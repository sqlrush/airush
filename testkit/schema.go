package testkit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"testing"

	// pgx stdlib 驱动注册 database/sql 的 "pgx" 驱动名。
	_ "github.com/jackc/pgx/v5/stdlib"
)

// NewSchema 在容器上创建随机命名 schema（t_<8位hex>）并返回定向连接串
// （search_path 指向该 schema）；测试结束自动 DROP（spec-0.5 §2.2 数据隔离约定）。
func (p *Postgres) NewSchema(ctx context.Context, t *testing.T) string {
	t.Helper()

	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}
	name := "t_" + hex.EncodeToString(buf)

	db, err := sql.Open("pgx", p.ConnString)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %q", name)); err != nil {
		t.Fatalf("create schema %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf("DROP SCHEMA %q CASCADE", name))
		_ = db.Close()
	})
	return p.ConnString + "&search_path=" + name
}
