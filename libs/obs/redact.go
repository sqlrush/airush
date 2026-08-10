package obs

import (
	"context"
	"log/slog"
	"regexp"
)

// redaction 兜底（spec-0.9 §2.1）：spec-0.7 类型防线之外的第二道防线。
// 保守模式：仅匹配高置信 secret 形态，误伤率优先于查全率；
// 扩充模式必须附误伤评估（spec-0.9 §6）。
var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|pwd)=[^\s&"']+`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                             // AWS access key
	regexp.MustCompile(`(?i)(sk|ghp|gho|xox[bp])-[A-Za-z0-9\-_]{16,}`), // 常见 API token 前缀
	regexp.MustCompile(`postgres(ql)?://[^:\s]+:[^@\s]+@`),             // 连接串内嵌口令
}

const redactedMark = "***REDACTED***"

func maskString(s string) string {
	masked := s
	for _, re := range redactPatterns {
		masked = re.ReplaceAllString(masked, redactedMark)
	}
	return masked
}

// redactAttr 是 slog ReplaceAttr 钩子（单 handler 场景/测试用）。
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindString {
		return a
	}
	if masked := maskString(a.Value.String()); masked != a.Value.String() {
		return slog.String(a.Key, masked)
	}
	return a
}

// redactHandler 是 record 级打码包装器：置于 fanout 之外，保证 stdout 与
// OTLP（Loki）两条出口同享脱敏（2026-08-10 obs-smoke 实证 Loki 分支曾泄漏）。
type redactHandler struct{ inner slog.Handler }

func (h redactHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, maskString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(maskAttrValue(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	masked := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		masked[i] = maskAttrValue(a)
	}
	return redactHandler{inner: h.inner.WithAttrs(masked)}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{inner: h.inner.WithGroup(name)}
}

func maskAttrValue(a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindString {
		return a
	}
	if masked := maskString(a.Value.String()); masked != a.Value.String() {
		return slog.String(a.Key, masked)
	}
	return a
}
