package tenancy

import (
	"context"
	"testing"

	libtenancy "github.com/sqlrush/airush/libs/tenancy"
)

// TestForwardSharesKeyWithLibs：console 别名与 libs 实现必须共享同一把 ctx key。
// 若有人在本包重实现 WithTenant，这里立刻红——那会造成 console 注入、libs 取不到的静默断层。
func TestForwardSharesKeyWithLibs(t *testing.T) {
	ctx := WithTenant(context.Background(), "t-1")
	if id, ok := libtenancy.FromContext(ctx); !ok || id != "t-1" {
		t.Fatalf("libs 取不到 console 注入的租户：(%q,%v)", id, ok)
	}
	ctx = libtenancy.WithTenant(context.Background(), "t-2")
	if id, ok := FromContext(ctx); !ok || id != "t-2" {
		t.Fatalf("console 取不到 libs 注入的租户：(%q,%v)", id, ok)
	}
}
