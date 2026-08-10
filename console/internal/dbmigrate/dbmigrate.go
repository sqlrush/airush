// Package dbmigrate 封装控制面数据库迁移（spec-0.6 D1）：
// golang-migrate + 嵌入迁移文件，经 console migrate 子命令暴露。
// 执行约定（spec-0.6 Q3）：部署期单点执行（Helm hook / 手工），禁服务启动自动迁移。
package dbmigrate

import (
	"errors"
	"fmt"
	"strings"

	gomigrate "github.com/golang-migrate/migrate/v4"
	// pgx5 数据库驱动注册 "pgx5" URL scheme。
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/sqlrush/airush/console/migrations"
)

// validateArgs 前置校验子命令（无 DB 依赖，可单测）。
func validateArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", errors.New("用法: console migrate <up|down|version>")
	}
	switch args[0] {
	case "up", "down", "version":
		return args[0], nil
	default:
		return "", fmt.Errorf("未知子命令 %q（可用: up/down/version）", args[0])
	}
}

// RunWithURL 以显式连接串执行迁移子命令（连接串由 main 经 spec-0.7 配置框架供给，
// 集成测试直连本入口）。
func RunWithURL(dbURL string, args []string) error {
	sub, err := validateArgs(args)
	if err != nil {
		return err
	}

	m, err := newMigrator(dbURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	switch sub {
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
		return fmt.Errorf("未知子命令 %q", sub) // validateArgs 已拦，防御分支
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
