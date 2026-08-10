//go:build integration

package testkit

import (
	"context"
	"database/sql"
	"testing"
)

// TestPostgresSchemaIsolation spec-0.5 D5 冒烟：起容器→双随机 schema→
// 各自建表写读→互不可见（T1 主链路 + T4 数据隔离一体验证），兼作集成范本。
func TestPostgresSchemaIsolation(t *testing.T) {
	ctx := context.Background()

	pg, err := StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	connA := pg.NewSchema(ctx, t)
	connB := pg.NewSchema(ctx, t)

	dbA := open(t, connA)
	dbB := open(t, connB)

	mustExec(t, dbA, `CREATE TABLE probe (v text)`)
	mustExec(t, dbB, `CREATE TABLE probe (v text)`)
	mustExec(t, dbA, `INSERT INTO probe VALUES ('from-a')`)
	mustExec(t, dbB, `INSERT INTO probe VALUES ('from-b')`)

	if got := queryOne(t, dbA, `SELECT v FROM probe`); got != "from-a" {
		t.Fatalf("schema A sees %q, want from-a", got)
	}
	if got := queryOne(t, dbB, `SELECT v FROM probe`); got != "from-b" {
		t.Fatalf("schema B sees %q, want from-b", got)
	}
}

func open(t *testing.T, conn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", conn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

func queryOne(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(q).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", q, err)
	}
	return v
}
