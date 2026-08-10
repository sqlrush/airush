// Package testkit 提供集成测试容器封装（spec-0.5 D2）：
// 容器包级复用 + schema 每用例隔离（spec-0.5 §2.2）；仅测试依赖，不进产物。
package testkit

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgImage 与 dev-deps compose、生产目标版本一致（spec-0.5 §2.2，升级修订 spec）。
const pgImage = "postgres:16.6"

// Postgres 是一个测试用 PG 容器句柄。
type Postgres struct {
	container *tcpostgres.PostgresContainer

	// ConnString 指向容器内 airush 库（postgres 用户，sslmode=disable）。
	ConnString string
}

// StartPostgres 启动 PG 容器；调用方在包测试收尾调用 Terminate 回收
// （异常退出由 testcontainers ryuk 兜底回收）。
func StartPostgres(ctx context.Context) (*Postgres, error) {
	c, err := tcpostgres.Run(ctx, pgImage,
		tcpostgres.WithDatabase("airush"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("airush_test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}
	conn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("resolve postgres conn string: %w", err)
	}
	return &Postgres{container: c, ConnString: conn}, nil
}

// Terminate 停止并移除容器。
func (p *Postgres) Terminate(ctx context.Context) error {
	if err := p.container.Terminate(ctx); err != nil {
		return fmt.Errorf("terminate postgres container: %w", err)
	}
	return nil
}
