// Package dbmigrate 封装控制面数据库迁移（spec-0.6 D1）：
// golang-migrate + 嵌入迁移文件，经 console migrate 子命令暴露。
// 执行约定（spec-0.6 Q3）：部署期单点执行（Helm hook / 手工），禁服务启动自动迁移。
package dbmigrate

import (
	"errors"
	"fmt"
	"os"
	"strings"

	gomigrate "github.com/golang-migrate/migrate/v4"
	// pgx5 数据库驱动注册 "pgx5" URL scheme。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/sqlrush/airush/console/migrations"
)

// envDBURL 是控制面库连接串环境变量（命名遵循 spec-0.7 §2.1 约定）。
const envDBURL = "AIRUSH_CONSOLE_DB_URL"

// Run 执行迁移子命令（up / down / version），连接串取自环境变量。
func Run(args []string) error {
	dbURL := os.Getenv(envDBURL)
	if dbURL == "" {
		return fmt.Errorf("环境变量 %s 未设置（控制面 PG 连接串，如 postgres://user:pass@host:5432/airush）", envDBURL)
	}
	return RunWithURL(dbURL, args)
}

// RunWithURL 以显式连接串执行迁移子命令（集成测试入口）。
func RunWithURL(dbURL string, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: console migrate <up|down|version>")
	}

	m, err := newMigrator(dbURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	switch args[0] {
	case "up":
		if err := m.Up(); err != nil {
			if errors.Is(err, gomigrate.ErrNoChange) {
				fmt.Println("migrate: already up to date")
				return nil
			}
			return fmt.Errorf("migrate up: %w", err)
		}
		fmt.Println("migrate: up ok")
		return nil
	case "down":
		// 单步回滚（开发/测试用；生产禁跑 down，回退=前滚新迁移，spec-0.6 §6）
		if err := m.Steps(-1); err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		fmt.Println("migrate: down 1 step ok")
		return nil
	case "version":
		v, dirty, err := m.Version()
		if errors.Is(err, gomigrate.ErrNilVersion) {
			fmt.Println("migrate: no migrations applied")
			return nil
		}
		if err != nil {
			return fmt.Errorf("migrate version: %w", err)
		}
		fmt.Printf("migrate: version=%d dirty=%v\n", v, dirty)
		return nil
	default:
		return fmt.Errorf("未知子命令 %q（可用: up/down/version）", args[0])
	}
}

// newMigrator 组装嵌入迁移源与 pgx5 目标库。
func newMigrator(dbURL string) (*gomigrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}
	m, err := gomigrate.NewWithSourceInstance("iofs", src, toPgx5URL(dbURL))
	if err != nil {
		return nil, fmt.Errorf("init migrator: %w", err)
	}
	return m, nil
}

// toPgx5URL 把常见 postgres:// 连接串改写为 golang-migrate pgx5 驱动的 scheme。
func toPgx5URL(dbURL string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dbURL, prefix) {
			return "pgx5://" + strings.TrimPrefix(dbURL, prefix)
		}
	}
	return dbURL
}
