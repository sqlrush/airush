// Package tenancy 是租户上下文的唯一注入/取用点（development-standards §2）。
//
// 2026-08-15 自 console/internal/tenancy 提到 libs（spec-1.7 §5，纯搬移）：libs/llm 与
// 将来的 agent-runtime 都要在 ctx 里带租户，而 libs 不得依赖 console。console 内保留
// 同名包做别名转发，既有 15 处调用零改动。
//
// Stage 1 租户来源 = 配置默认租户；spec-2.2 起由认证态替换注入方，本包接口不变。
package tenancy

import "context"

type ctxKey struct{}

// WithTenant 返回携带租户 id 的新 ctx（不修改传入 ctx）。
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

// FromContext 取出租户 id；未注入或空值返回 false。
func FromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok && id != ""
}
