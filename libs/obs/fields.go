package obs

import (
	"context"
	"log/slog"
)

// ensureFields 保底注入必带字段（development-standards §1.5）：
// tenant_id / trace_id 未由调用链提供时补 "-"；已提供（经 With 或行内属性）
// 则不重复注入，避免 JSON 重复键。
type ensureFields struct {
	inner     slog.Handler
	hasTenant bool
	hasTrace  bool
}

func (e ensureFields) Enabled(ctx context.Context, l slog.Level) bool {
	return e.inner.Enabled(ctx, l)
}

func (e ensureFields) Handle(ctx context.Context, r slog.Record) error {
	hasTenant, hasTrace := e.hasTenant, e.hasTrace
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "tenant_id":
			hasTenant = true
		case "trace_id":
			hasTrace = true
		}
		return true
	})
	if !hasTenant {
		r.AddAttrs(slog.String("tenant_id", "-"))
	}
	if !hasTrace {
		r.AddAttrs(slog.String("trace_id", "-"))
	}
	return e.inner.Handle(ctx, r)
}

func (e ensureFields) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := ensureFields{
		inner:     e.inner.WithAttrs(attrs),
		hasTenant: e.hasTenant,
		hasTrace:  e.hasTrace,
	}
	for _, a := range attrs {
		switch a.Key {
		case "tenant_id":
			next.hasTenant = true
		case "trace_id":
			next.hasTrace = true
		}
	}
	return next
}

func (e ensureFields) WithGroup(name string) slog.Handler {
	return ensureFields{inner: e.inner.WithGroup(name), hasTenant: e.hasTenant, hasTrace: e.hasTrace}
}
