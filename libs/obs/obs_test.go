package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// T2：必带字段 schema——component / tenant_id="-" / trace_id="-"。
func TestBaseFieldsPresent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr})
	logger := withBaseFields(h, Config{Component: "gateway"})

	logger.Info("hello")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("parse log: %v", err)
	}
	if rec["component"] != "gateway" || rec["tenant_id"] != "-" || rec["trace_id"] != "-" {
		t.Fatalf("base fields wrong: %v", rec)
	}
}

// T3：redaction 兜底——已知 secret 形态打码。
func TestRedaction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"password_kv", "connect with password=hunter2 now"},
		{"bearer", "auth Bearer eyJhbGciOi.some.token"},
		{"aws_key", "found AKIAIOSFODNN7EXAMPLE in env"},
		{"conn_string", "dsn postgres://app:s3cret@db:5432/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr})
			slog.New(h).Info("event", "detail", tc.in)

			out := buf.String()
			if !strings.Contains(out, redactedMark) {
				t.Fatalf("not redacted: %s", out)
			}
			for _, leak := range []string{"hunter2", "s3cret", "AKIAIOSFODNN7EXAMPLE"} {
				if strings.Contains(out, leak) {
					t.Fatalf("leaked %q: %s", leak, out)
				}
			}
		})
	}
}

// T4：label 白名单构造期 fail-fast。
func TestLabelWhitelistPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for tenant_id label")
		}
	}()
	_ = Labels("tenant_id", "t-123") // 高基数禁入（spec-0.9 §2.2）
}

// T5：无端点/坏端点均不阻断初始化（降级契约）。
func TestInitDegradesGracefully(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p1 := Init(ctx, Config{Component: "t", LogLevel: "info"})
	if p1 == nil || p1.Logger == nil {
		t.Fatal("logs-only init failed")
	}
	p2 := Init(ctx, Config{Component: "t", LogLevel: "info",
		OTLPEndpoint: "127.0.0.1:1", SampleRatio: 1})
	if p2 == nil || p2.Logger == nil {
		t.Fatal("bogus endpoint init should still return provider")
	}
	shutdownCtx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer c2()
	p2.Shutdown(shutdownCtx)
}

// ctx logger 注取回路。
func TestContextLogger(t *testing.T) {
	t.Parallel()
	base := slog.Default().With("trace_id", "tr_ctx")
	ctx := ContextWithLogger(context.Background(), base)
	if LoggerFrom(ctx) != base {
		t.Fatal("logger roundtrip failed")
	}
	if LoggerFrom(context.Background()) == nil {
		t.Fatal("fallback logger missing")
	}
}
