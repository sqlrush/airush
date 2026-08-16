// Package tenancy 是 libs/tenancy 的别名转发（spec-1.7 §5，2026-08-15）。
//
// 实现已提到 libs/tenancy（libs/llm、agent-runtime 也要用，而 libs 不得依赖 console）。
// 本包只剩转发，让 console 内既有 15 处 import 零改动；新代码请直接 import libs/tenancy。
// ctx key 是 libs 包内的私有类型，两边取放的是同一把 key——不存在"两套上下文"。
package tenancy

import (
	"context"

	"github.com/sqlrush/airush/libs/tenancy"
)

// WithTenant 见 libs/tenancy.WithTenant。
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return tenancy.WithTenant(ctx, tenantID)
}

// FromContext 见 libs/tenancy.FromContext。
func FromContext(ctx context.Context) (string, bool) { return tenancy.FromContext(ctx) }
