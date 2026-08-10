package testkit

import (
	"context"
	"fmt"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// redisImage 与 dev-deps compose 版本一致（spec-0.5 §2.2）。
const redisImage = "redis:7.4"

// Redis 是一个测试用 Redis 容器句柄。
type Redis struct {
	container *tcredis.RedisContainer

	// URL 形如 redis://host:port。
	URL string
}

// StartRedis 启动 Redis 容器；回收语义同 StartPostgres。
func StartRedis(ctx context.Context) (*Redis, error) {
	c, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		return nil, fmt.Errorf("start redis container: %w", err)
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve redis conn string: %w", err)
	}
	return &Redis{container: c, URL: url}, nil
}

// Terminate 停止并移除容器。
func (r *Redis) Terminate(ctx context.Context) error {
	if err := r.container.Terminate(ctx); err != nil {
		return fmt.Errorf("terminate redis container: %w", err)
	}
	return nil
}
